package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Options configura il logger.
type Options struct {
	// Path è il file con i messaggi ordinari. La cartella che lo contiene
	// viene creata se manca. Quando Verbose è falso il file non viene nemmeno
	// creato, a meno che non serva anche per gli allarmi.
	Path string
	// AlertPath è il file che riceve i soli record di allarme. Se coincide
	// con Path i due logger condividono lo stesso file.
	AlertPath string
	// Level è debug, info, warn oppure error.
	Level string
	// Stdout, se non è nil, riceve il flusso leggibile oltre a quello
	// strutturato scritto in Path.
	Stdout io.Writer
	// Verbose abilita i messaggi di funzionamento, su console e su file. Se è
	// falso il programma tace: sul terminale compare soltanto la riga di
	// allarme, e nessuna parola pronunciata viene scritta da qualche parte.
	//
	// Il silenzio è applicato qui e non nei chiamanti, così è impossibile
	// dimenticarsi di togliere una stampa: tutto ciò che passa da Console o da
	// File viene scartato, comprese le trascrizioni.
	Verbose bool
}

// Logger espone separatamente le tre destinazioni, perché non devono essere
// mescolate: Console serve a chi guarda il terminale, File conserva la
// traccia di funzionamento e Alerts contiene solo gli eventi che hanno fatto
// scattare l'allarme, così quel file può essere seguito o spedito da solo.
//
// Quando le opzioni non sono verbose, Console e File sono costruiti su
// io.Discard: restano utilizzabili e non Nil, ma la loro scrittura non lascia
// traccia da nessuna parte.
type Logger struct {
	// Console scrive sia su Stdout sia su Path.
	Console *slog.Logger
	// File scrive solo su Path.
	File *slog.Logger
	// Alerts scrive solo su AlertPath.
	Alerts *slog.Logger

	closers []io.Closer
}

// Close chiude i file di log.
func (l *Logger) Close() error {
	var firstErr error
	for _, c := range l.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// New apre i file di log e restituisce i logger per console, file e allarmi.
// Il file ordinario riceve record JSON con indicatore di tempo; la console
// riceve record di testo preceduti dal proprio orario.
//
// Senza opts.Verbose il file ordinario non viene creato e console e file
// scartano ogni record: sul terminale resta solo ciò che scrive direttamente
// l'alerter, cioè la riga di emergenza. In quel caso il file viene aperto
// solo se gli allarmi devono finire lì.
func New(opts Options) (*Logger, error) {
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	// Gli allarmi finiscono nel file ordinario solo se non ne hanno uno
	// dedicato: in quel caso il file serve comunque e va aperto.
	alertsShareMain := opts.AlertPath == "" || opts.AlertPath == opts.Path
	needMainFile := opts.Verbose || alertsShareMain

	l := &Logger{}
	var mainFile *os.File

	if needMainFile {
		f, err := open(opts.Path)
		if err != nil {
			return nil, err
		}
		mainFile = f
		l.closers = append(l.closers, f)
		l.File = slog.New(jsonHandler(f, level))

		var handlers []slog.Handler
		if opts.Verbose {
			handlers = append(handlers, jsonHandler(f, level))
			if opts.Stdout != nil {
				handlers = append(handlers, slog.NewTextHandler(
					&lockedWriter{w: opts.Stdout},
					&slog.HandlerOptions{Level: level, ReplaceAttr: withoutTime},
				))
			}
		}
		if len(handlers) == 0 {
			l.Console = discard()
		} else {
			l.Console = slog.New(&fanout{handlers: handlers})
		}
	} else {
		l.File = discard()
		l.Console = discard()
	}

	var alertWriter io.Writer = discardWriter{}
	if alertsShareMain {
		alertWriter = mainFile
	} else {
		f, err := open(opts.AlertPath)
		if err != nil {
			_ = l.Close()
			return nil, err
		}
		l.closers = append(l.closers, f)
		alertWriter = f
	}
	l.Alerts = slog.New(jsonHandler(alertWriter, level))

	return l, nil
}

// discardWriter è un io.Writer che scarta tutto. Serve al posto di
// io.Discard perché slog ne ha bisogno di un tipo stabile, non di un valore
// che può cambiare tra una versione e l'altra.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// discard restituisce un logger che scarta tutto. Non è mai nil, quindi i
// chiamanti non devono fare controlli.
func discard() *slog.Logger {
	return slog.New(slog.NewJSONHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func jsonHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewJSONHandler(&lockedWriter{w: w}, &slog.HandlerOptions{Level: level})
}

// open crea la cartella che contiene path e apre il file in modalità append.
func open(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logging: creazione directory %s fallita: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logging: apertura %s fallita: %w", path, err)
	}
	return f, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: livello %q non ammesso (debug, info, warn, error)", s)
	}
}

// withoutTime elimina dal logger di console la chiave "time" duplicata,
// perché la console la stampa già.
func withoutTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// fanout invia ogni record a un insieme di handler.
type fanout struct {
	handlers []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithAttrs(attrs))
	}
	return &fanout{handlers: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithGroup(name))
	}
	return &fanout{handlers: next}
}

// lockedWriter serializza le scritture, perché gli handler girano su più
// goroutine e os.File non è sicuro per scritture intercalate.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
