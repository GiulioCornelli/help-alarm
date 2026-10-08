package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/giulio/help-alarm-STT/internal/alert"
	"github.com/giulio/help-alarm-STT/internal/audio"
	"github.com/giulio/help-alarm-STT/internal/config"
	"github.com/giulio/help-alarm-STT/internal/keyword"
	"github.com/giulio/help-alarm-STT/internal/logging"
	"github.com/giulio/help-alarm-STT/internal/stt"
)

func main() {
	configPath := flag.String("config", "config.json", "percorso del file di configurazione")
	listDevices := flag.Bool("devices", false, "elenca i microfoni disponibili ed esci")

	// -v e -verbose sono lo stesso interruttore: il secondo è più esplicito
	// per chi non ricorda la forma breve.
	var verbose bool
	flag.BoolVar(&verbose, "v", false, "modalità dettagliata: mostra e salva tutto, trascrizioni comprese")
	flag.BoolVar(&verbose, "verbose", false, "come -v")
	flag.Parse()

	if *listDevices {
		if err := audio.ListDevices(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "errore:", err)
			os.Exit(1)
		}
		return
	}

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
		"modello", cfg.AbsModelPath(),
		"log_file", cfg.AbsLogPath(),
		"log_allarmi", alertLogPath(cfg),
		"cooldown", cfg.Cooldown().String(),
	)

	matcher := keyword.New(cfg.Keywords)
	if matcher.Empty() {
		return errors.New("nessuna parola chiave utilizzabile nella configurazione")
	}

	// L'alert riceve il logger del solo file allarmi: la riga sul terminale è
	// già scritta direttamente da lui e non deve essere duplicata.
	alerter := alert.NewTerminal(os.Stdout, logs.Alerts)
	cooldown := alert.NewCooldown(cfg.Cooldown())

	recognizer, err := stt.Open(cfg.AbsModelPath())
	if err != nil {
		return err
	}
	defer recognizer.Close()

	capture, err := audio.New(audio.Config{
		DeviceID:   cfg.DeviceID,
		SampleRate: uint32(cfg.SampleRate),
	})
	if err != nil {
		return err
	}

	// SIGINT e SIGTERM annullano il contesto, il che smonta le goroutine
	// qui sotto in ordine.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	frames := capture.Frames()
	transcribing := make(chan struct{})
	go func() {
		defer close(transcribing)

		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-frames:
				if !ok {
					return
				}
				phrase, final, err := recognizer.Accept(frame)
				if err != nil {
					log.Error("errore durante la trascrizione", "errore", err)
					continue
				}
				if phrase.Text == "" {
					continue
				}
				if cfg.LogTranscriptions {
					if final {
						log.Info("trascrizione", "testo", phrase.Text)
					} else {
						log.Debug("ipotesi parziale", "testo", phrase.Text)
					}
				}
				// Si analizzano anche le ipotesi parziali: permettono di
				// reagire senza aspettare la fine della frase.
				trigger(phrase, matcher, alerter, cooldown, log)
			}
		}
	}()

	log.Info("microfono in ascolto", "dispositivo", capture.Description())

	if err := capture.Start(); err != nil {
		stop()
		<-transcribing
		return err
	}

	<-ctx.Done()
	log.Info("arresto in corso")

	capture.Stop()
	<-transcribing

	// Frase rimasta a mezz'aria: la si transcribe prima di chiudere.
	if phrase, err := recognizer.Flush(); err != nil {
		log.Error("errore durante la trascrizione finale", "errore", err)
	} else if phrase.Text != "" {
		log.Info("trascrizione", "testo", phrase.Text)
		trigger(phrase, matcher, alerter, cooldown, log)
	}

	log.Info("arresto completato")
	return nil
}

// trigger è il terzo stadio della catena: analizza la trascrizione e attiva
// l'alert per ogni parola chiave trovata.
func trigger(phrase stt.Phrase, matcher *keyword.Matcher, alerter alert.Alerter, cooldown *alert.Cooldown, log *slog.Logger) {
	for _, m := range matcher.Find(phrase.Text) {
		now := time.Now()
		// Vosk rilancia la stessa parola a ogni blocco di 100 ms finché la
		// persona parla: senza questo filtro un solo "aiuto" produrrebbe una
		// raffica di righe identiche.
		if !cooldown.Allow(m.Keyword, now) {
			log.Debug("allarme soppresso dal cooldown", "keyword", m.Keyword, "trascrizione", m.Phrase)
			continue
		}
		ev := alert.Event{
			Keyword: m.Keyword,
			At:      now,
		}
		if err := alerter.Trigger(ev); err != nil {
			log.Error("errore durante l'attivazione dell'allarme", "errore", err)
		}
	}
}

// alertLogPath indica il file che riceverà gli allarmi, per la riga di avvio.
func alertLogPath(cfg *config.Config) string {
	if p := cfg.AbsAlertLogPath(); p != "" {
		return p
	}
	return cfg.AbsLogPath() + " (stesso file dei messaggi normali)"
}
