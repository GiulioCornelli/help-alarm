package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/giulio/help-alarm-STT/internal/alert"
	"github.com/giulio/help-alarm-STT/internal/config"
	"github.com/giulio/help-alarm-STT/internal/keyword"
	"github.com/giulio/help-alarm-STT/internal/logging"
	"github.com/giulio/help-alarm-STT/internal/rtsp"
	"github.com/giulio/help-alarm-STT/internal/stt"
)

func main() {
	configPath := flag.String("config", "config.json", "percorso del file di configurazione")

	// -v e -verbose sono lo stesso interruttore: il secondo è più esplicito
	// per chi non ricorda la forma breve.
	var verbose bool
	flag.BoolVar(&verbose, "v", false, "modalità dettagliata: mostra e salva tutto, trascrizioni comprese")
	flag.BoolVar(&verbose, "verbose", false, "come -v")
	flag.Parse()

	if err := run(*configPath, verbose); err != nil {
		fmt.Fprintln(os.Stderr, "errore:", err)
		os.Exit(1)
	}
}

// run avvia il programma. Senza verbose il terminale resta muto e sul disco
// finiscono solo i record di allarme: nessuna parola pronunciata viene scritta
// o mostrata, a meno di non essere una parola chiave configurata.
func run(configPath string, verbose bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// Il silenzio è deciso qui e resta valido per tutto il programma: con
	// verbose falso Console e File scartano ogni record.
	logs, err := logging.New(logging.Options{
		Path:      cfg.AbsLogPath(),
		AlertPath: cfg.AbsAlertLogPath(),
		Level:     cfg.LogLevel,
		Stdout:    os.Stdout,
		Verbose:   verbose,
	})
	if err != nil {
		return err
	}
	defer logs.Close()

	log := logs.Console

	log.Info("avvio help-alarm",
		"config", configPath,
		"keywords", cfg.Keywords,
		"telecamere", cameraList(cfg.Cameras),
		"modello", cfg.AbsModelPath(),
		"log_file", cfg.AbsLogPath(),
		"log_allarmi", alertLogPath(cfg),
		"cooldown", cfg.Cooldown().String(),
	)

	matcher := keyword.New(cfg.Keywords)
	if matcher.Empty() {
		return errors.New("nessuna parola chiave utilizzabile nella configurazione")
	}

	// ffmpeg è installato solo nell'immagine Docker: fuori dal container
	// manca, e senza questo controllo ogni telecamera riproverebbe per
	// sempre con un errore poco chiaro.
	if _, err := exec.LookPath(rtsp.FFmpeg); err != nil {
		return errors.New("ffmpeg non trovato: il programma va avviato con Docker (docker compose up -d), l'immagine lo contiene")
	}

	// L'alert riceve il logger del solo file allarmi: la riga sul terminale è
	// già scritta direttamente da lui e non deve essere duplicata.
	alerter := alert.NewTerminal(os.Stdout, logs.Alerts)
	cooldown := alert.NewCooldown(cfg.Cooldown())

	// Il modello si carica una volta sola; ogni telecamera ha il proprio
	// recognizer, perché tiene lo stato della frase in corso. I recognizer
	// vengono liberati prima del modello: i defer girano in ordine inverso.
	model, err := stt.LoadModel(cfg.AbsModelPath())
	if err != nil {
		return err
	}
	defer model.Close()

	var recognizers []*stt.Recognizer
	defer func() {
		for _, r := range recognizers {
			r.Close()
		}
	}()
	for range cfg.Cameras {
		rec, err := stt.NewRecognizer(model)
		if err != nil {
			return err
		}
		recognizers = append(recognizers, rec)
	}

	// SIGINT e SIGTERM annullano il contesto: ogni ffmpeg viene fermato, il
	// suo canale si chiude e la goroutine della telecamera termina.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	for i, cam := range cfg.Cameras {
		host := rtsp.Host(cam.URL)
		l := &listener{
			camera:            cam.Name,
			host:              host,
			rec:               recognizers[i],
			matcher:           matcher,
			alerter:           alerter,
			cooldown:          cooldown,
			logTranscriptions: cfg.LogTranscriptions,
			log:               log.With("camera", cam.Name, "camera_ip", host),
		}
		stream := &rtsp.Stream{
			Name:       cam.Name,
			URL:        cam.URL,
			SampleRate: cfg.SampleRate,
			Log:        l.log,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.listen(stream.Frames(ctx))
		}()
	}

	log.Info("telecamere in ascolto", "numero", len(cfg.Cameras))

	<-ctx.Done()
	log.Info("arresto in corso")
	wg.Wait()
	log.Info("arresto completato")
	return nil
}

// listener è la catena di una telecamera: trascrizione, ricerca delle parole
// chiave e allarme. Ogni telecamera ne ha uno, sulla propria goroutine;
// matcher, alerter e cooldown sono condivisi e sicuri per l'uso concorrente.
type listener struct {
	camera            string
	host              string
	rec               *stt.Recognizer
	matcher           *keyword.Matcher
	alerter           alert.Alerter
	cooldown          *alert.Cooldown
	logTranscriptions bool
	log               *slog.Logger
}

// listen trascrive i blocchi di audio finché il canale resta aperto.
func (l *listener) listen(frames <-chan []byte) {
	for frame := range frames {
		phrase, final, err := l.rec.Accept(frame)
		if err != nil {
			l.log.Error("errore durante la trascrizione", "errore", err)
			continue
		}
		if phrase.Text == "" {
			continue
		}
		if l.logTranscriptions {
			if final {
				l.log.Info("trascrizione", "testo", phrase.Text)
			} else {
				l.log.Debug("ipotesi parziale", "testo", phrase.Text)
			}
		}
		// Si analizzano anche le ipotesi parziali: permettono di reagire
		// senza aspettare la fine della frase.
		l.trigger(phrase)
	}

	// Frase rimasta a mezz'aria: la si trascrive prima di chiudere.
	if phrase, err := l.rec.Flush(); err != nil {
		l.log.Error("errore durante la trascrizione finale", "errore", err)
	} else if phrase.Text != "" {
		if l.logTranscriptions {
			l.log.Info("trascrizione", "testo", phrase.Text)
		}
		l.trigger(phrase)
	}
}

// trigger è il terzo stadio della catena: analizza la trascrizione e attiva
// l'alert per ogni parola chiave trovata.
func (l *listener) trigger(phrase stt.Phrase) {
	for _, m := range l.matcher.Find(phrase.Text) {
		now := time.Now()
		// Vosk rilancia la stessa parola a ogni blocco di 100 ms finché la
		// persona parla: senza questo filtro un solo "aiuto" produrrebbe una
		// raffica di righe identiche.
		if !l.cooldown.Allow(l.camera, m.Keyword, now) {
			l.log.Debug("allarme soppresso dal cooldown", "keyword", m.Keyword, "trascrizione", m.Phrase)
			continue
		}
		ev := alert.Event{
			Keyword: m.Keyword,
			Camera:  l.camera,
			Host:    l.host,
			At:      now,
		}
		if err := l.alerter.Trigger(ev); err != nil {
			l.log.Error("errore durante l'attivazione dell'allarme", "errore", err)
		}
	}
}

// cameraList descrive le telecamere per la riga di avvio: nome e indirizzo,
// mai l'URL, che può contenere la password.
func cameraList(cams []config.Camera) []string {
	out := make([]string, 0, len(cams))
	for _, c := range cams {
		out = append(out, c.Name+" ("+rtsp.Host(c.URL)+")")
	}
	return out
}

// alertLogPath indica il file che riceverà gli allarmi, per la riga di avvio.
func alertLogPath(cfg *config.Config) string {
	if p := cfg.AbsAlertLogPath(); p != "" {
		return p
	}
	return cfg.AbsLogPath() + " (stesso file dei messaggi normali)"
}
