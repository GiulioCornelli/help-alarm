// Package rtsp riceve l'audio di una telecamera IP e lo consegna come PCM
// grezzo, nel formato richiesto da Vosk.
//
// La decodifica è affidata a ffmpeg, avviato come processo separato per ogni
// telecamera: si collega allo stream RTSP, estrae la traccia audio qualunque
// sia il codec (AAC, G.711, ...) e la converte in PCM s16le mono a 16 kHz
// sul proprio stdout. Il programma legge solo i byte già pronti.
//
// ffmpeg vive esclusivamente nell'immagine Docker: sull'host non serve.
package rtsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// FFmpeg è il nome dell'eseguibile cercato nel PATH.
const FFmpeg = "ffmpeg"

const (
	// frameDuration è la durata di un blocco di audio consegnato a valle:
	// la stessa cadenza che usava il microfono, quindi Vosk emette una
	// trascrizione ogni 100 ms come prima.
	frameDuration = 100 * time.Millisecond
	// bytesPerSample: PCM s16, mono.
	bytesPerSample = 2
	// stderrLimit è quanto output di errore di ffmpeg viene conservato per
	// il log: basta per il messaggio finale, che è quello utile.
	stderrLimit = 2048
	// ioTimeout è il tempo oltre il quale ffmpeg considera morta una
	// telecamera che non manda più dati. Senza, una telecamera che smette di
	// trasmettere senza chiudere la connessione bloccherebbe la lettura per
	// sempre.
	ioTimeout = 10 * time.Second
)

// Stream descrive una telecamera da ascoltare.
type Stream struct {
	// Name identifica la telecamera nei log.
	Name string
	// URL è l'indirizzo RTSP, credenziali comprese. Non deve mai finire nei
	// log: usare Redact per tutto ciò che ne può contenere una copia.
	URL string
	// SampleRate in Hz dell'audio prodotto. Vosk richiede 16000.
	SampleRate int
	// Log riceve i messaggi di funzionamento: connessione, interruzioni,
	// nuovi tentativi.
	Log *slog.Logger

	// MinBackoff e MaxBackoff limitano l'attesa fra due tentativi di
	// connessione. L'attesa raddoppia a ogni fallimento consecutivo e torna
	// al minimo quando la telecamera ha mandato audio. Se sono zero valgono
	// 1 s e 30 s.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// command costruisce il processo di decodifica. È sostituibile nei test,
	// che così girano senza ffmpeg e senza telecamere.
	command func(ctx context.Context, s *Stream) *exec.Cmd
}

// Frames avvia la ricezione e restituisce il canale dei blocchi di PCM, da
// 100 ms ciascuno. Se ffmpeg termina, per una telecamera irraggiungibile o
// uno stream interrotto, viene riavviato dopo un'attesa crescente: una
// telecamera offline non ferma mai le altre.
//
// Il canale viene chiuso quando ctx è annullato e il processo è terminato.
func (s *Stream) Frames(ctx context.Context) <-chan []byte {
	// 8 slot: abbastanza margine per non perdere frame se la trascrizione
	// rallenta per un istante.
	ch := make(chan []byte, 8)
	go func() {
		defer close(ch)
		s.loop(ctx, ch)
	}()
	return ch
}

func (s *Stream) loop(ctx context.Context, ch chan<- []byte) {
	minWait, maxWait := s.MinBackoff, s.MaxBackoff
	if minWait <= 0 {
		minWait = time.Second
	}
	if maxWait < minWait {
		maxWait = 30 * time.Second
		if maxWait < minWait {
			maxWait = minWait
		}
	}

	wait := minWait
	for {
		received, err := s.runOnce(ctx, ch)
		if ctx.Err() != nil {
			return
		}
		// La telecamera ha mandato audio: l'interruzione è un incidente,
		// non una telecamera spenta, quindi si riprova subito al minimo.
		if received {
			wait = minWait
		}
		s.Log.Warn("stream interrotto, nuovo tentativo",
			"errore", err,
			"attesa", wait.String(),
		)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if !received {
			wait *= 2
			if wait > maxWait {
				wait = maxWait
			}
		}
	}
}

// runOnce avvia un processo di decodifica e ne legge l'audio finché termina.
// Restituisce se è arrivato almeno un blocco e il motivo della fine, già
// ripulito dalle credenziali.
func (s *Stream) runOnce(ctx context.Context, ch chan<- []byte) (bool, error) {
	build := s.command
	if build == nil {
		build = ffmpegCommand
	}
	cmd := build(ctx, s)

	stderr := &tailBuffer{limit: stderrLimit}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("rtsp: pipe di ffmpeg: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("rtsp: avvio ffmpeg fallito: %w", err)
	}

	frameSize := s.frameSize()
	received := false
	var readErr error
	for {
		buf := make([]byte, frameSize)
		if _, err := io.ReadFull(stdout, buf); err != nil {
			readErr = err
			break
		}
		if !received {
			received = true
			s.Log.Info("telecamera connessa")
		}
		// Invio non bloccante: se la trascrizione è in ritardo il blocco
		// viene scartato. Un frame perso (100 ms) è preferibile a un audio
		// che resta indietro rispetto alla realtà, cosa che in un sistema
		// di allarme ritarderebbe ogni segnalazione successiva.
		select {
		case ch <- buf:
		default:
			s.Log.Debug("frame scartato: trascrizione in ritardo")
		}
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return received, ctx.Err()
	}

	msg := strings.TrimSpace(stderr.String())
	switch {
	case msg != "":
		return received, errors.New(Redact(msg, s.URL))
	case waitErr != nil:
		return received, fmt.Errorf("ffmpeg terminato: %s", Redact(waitErr.Error(), s.URL))
	case errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF):
		return received, errors.New("lo stream è terminato")
	default:
		return received, readErr
	}
}

func (s *Stream) frameSize() int {
	rate := s.SampleRate
	if rate <= 0 {
		rate = 16000
	}
	return rate * bytesPerSample * int(frameDuration/time.Millisecond) / 1000
}

// ffmpegCommand costruisce il comando di decodifica di una telecamera.
func ffmpegCommand(ctx context.Context, s *Stream) *exec.Cmd {
	rate := s.SampleRate
	if rate <= 0 {
		rate = 16000
	}
	args := []string{
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
		// TCP invece di UDP: niente pacchetti persi o riordinati, e passa
		// anche dove l'UDP è filtrato.
		"-rtsp_transport", "tcp",
		// Timeout delle operazioni di rete, in microsecondi.
		"-timeout", fmt.Sprint(ioTimeout.Microseconds()),
		"-i", s.URL,
		// Solo la prima traccia audio: il video non viene decodificato. Se
		// la telecamera non ha audio, ffmpeg esce con un errore esplicito.
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", fmt.Sprint(rate),
		"-f", "s16le",
		"pipe:1",
	}
	cmd := exec.CommandContext(ctx, FFmpeg, args...)
	// All'arresto ffmpeg riceve SIGKILL subito: non ha stato da salvare.
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Host restituisce l'indirizzo (IP o nome) della telecamera, senza porta né
// credenziali. È ciò che può comparire nei log.
func Host(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Redact toglie da text ogni copia delle credenziali contenute in rawURL.
//
// ffmpeg ripete l'URL nei propri messaggi di errore, password compresa:
// tutto ciò che arriva da ffmpeg o contiene l'URL passa da qui prima di
// finire in un log.
func Redact(text, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return text
	}
	safe := *u
	safe.User = nil
	text = strings.ReplaceAll(text, rawURL, safe.String())
	// Le credenziali possono comparire anche da sole, in forma codificata
	// (come nell'URL) o decodificata.
	text = strings.ReplaceAll(text, u.User.String()+"@", "")
	if pwd, ok := u.User.Password(); ok && pwd != "" {
		text = strings.ReplaceAll(text, pwd, "***")
	}
	return text
}

// tailBuffer conserva solo gli ultimi limit byte scritti: ffmpeg può
// ripetere lo stesso errore all'infinito e non serve tenerlo tutto.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
