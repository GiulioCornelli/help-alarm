package rtsp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// I test non usano ffmpeg: il processo di decodifica è sostituito dal binario
// stesso dei test, rilanciato in modalità "aiutante" (TestHelperProcess), che
// scrive su stdout blocchi di PCM finti.

const helperEnv = "HELP_ALARM_RTSP_HELPER"

// TestHelperProcess non è un vero test: è il finto ffmpeg.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	frame := bytes.Repeat([]byte{1}, 3200)
	switch mode {
	case "frames":
		// Tre blocchi poi chiude: simula uno stream che cade.
		for i := 0; i < 3; i++ {
			os.Stdout.Write(frame)
		}
	case "forever":
		for {
			if _, err := os.Stdout.Write(frame); err != nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "fail":
		// Come ffmpeg con una telecamera irraggiungibile: l'URL, password
		// compresa, finisce nel messaggio di errore.
		fmt.Fprintln(os.Stderr, os.Getenv("HELP_ALARM_RTSP_URL")+": Connection refused")
		os.Exit(1)
	}
	os.Exit(0)
}

func helper(mode string) func(ctx context.Context, s *Stream) *exec.Cmd {
	return func(ctx context.Context, s *Stream) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), helperEnv+"="+mode, "HELP_ALARM_RTSP_URL="+s.URL)
		return cmd
	}
}

func newStream(mode string, log *slog.Logger) *Stream {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Stream{
		Name:       "test",
		URL:        "rtsp://admin:segreta@192.168.1.10:554/stream1",
		SampleRate: 16000,
		Log:        log,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
		command:    helper(mode),
	}
}

func TestFramesAre100ms(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frames := newStream("forever", nil).Frames(ctx)
	frame := <-frames
	// 16000 campioni/s * 2 byte * 0,1 s
	if len(frame) != 3200 {
		t.Errorf("frame di %d byte, attesi 3200", len(frame))
	}
}

// TestReconnects verifica che uno stream caduto venga riaperto da solo: il
// finto ffmpeg manda tre blocchi e termina, quindi riceverne più di tre vuol
// dire che è stato rilanciato.
func TestReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frames := newStream("frames", nil).Frames(ctx)
	for i := 0; i < 7; i++ {
		select {
		case <-frames:
		case <-ctx.Done():
			t.Fatalf("ricevuti %d frame prima del timeout: lo stream non è stato riaperto", i)
		}
	}
}

func TestCancelClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	frames := newStream("forever", nil).Frames(ctx)
	<-frames
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("il canale non è stato chiuso dopo l'annullamento")
		}
	}
}

// TestErrorsDoNotLeakPassword presidia le credenziali: l'errore di ffmpeg
// contiene l'URL completo, ma nel log non deve comparire la password.
func TestErrorsDoNotLeakPassword(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&lockedBuffer{b: &buf}, nil))

	ctx, cancel := context.WithCancel(context.Background())
	s := newStream("fail", log)
	frames := s.Frames(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	for range frames {
	}

	out := buf.String()
	if !strings.Contains(out, "Connection refused") {
		t.Fatalf("l'errore di ffmpeg non è nel log: %q", out)
	}
	if strings.Contains(out, "segreta") || strings.Contains(out, "admin") {
		t.Errorf("le credenziali sono finite nel log: %q", out)
	}
	if !strings.Contains(out, "192.168.1.10") {
		t.Errorf("il log non indica la telecamera: %q", out)
	}
}

func TestRedact(t *testing.T) {
	const raw = "rtsp://admin:p%40ss@10.0.0.5:554/live"
	cases := []struct{ in, want string }{
		{raw + ": 401 Unauthorized", "rtsp://10.0.0.5:554/live: 401 Unauthorized"},
		{"utente admin:p%40ss@10.0.0.5", "utente 10.0.0.5"},
		{"password p@ss rifiutata", "password *** rifiutata"},
		{"nessuna credenziale", "nessuna credenziale"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in, raw); got != tc.want {
			t.Errorf("Redact(%q) = %q, atteso %q", tc.in, got, tc.want)
		}
	}

	// Senza credenziali il testo resta com'è.
	if got := Redact("rtsp://10.0.0.5/live", "rtsp://10.0.0.5/live"); got != "rtsp://10.0.0.5/live" {
		t.Errorf("Redact senza credenziali = %q", got)
	}
}

func TestHost(t *testing.T) {
	cases := map[string]string{
		"rtsp://admin:pwd@192.168.1.10:554/stream1": "192.168.1.10",
		"rtsp://cam-ingresso.lan/live":              "cam-ingresso.lan",
		"rtsps://[fe80::1]:322/x":                   "fe80::1",
	}
	for in, want := range cases {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, atteso %q", in, got, want)
		}
	}
}

func TestFFmpegCommand(t *testing.T) {
	s := &Stream{URL: "rtsp://10.0.0.5/live", SampleRate: 16000}
	args := strings.Join(ffmpegCommand(context.Background(), s).Args, " ")
	for _, want := range []string{"-i rtsp://10.0.0.5/live", "-map 0:a:0", "-ac 1", "-ar 16000", "-f s16le", "pipe:1"} {
		if !strings.Contains(args, want) {
			t.Errorf("comando ffmpeg %q non contiene %q", args, want)
		}
	}
}

func TestTailBufferKeepsLast(t *testing.T) {
	b := &tailBuffer{limit: 5}
	b.Write([]byte("abc"))
	b.Write([]byte("defgh"))
	if b.String() != "defgh" {
		t.Errorf("tailBuffer = %q, atteso \"defgh\"", b.String())
	}
}

// lockedBuffer rende bytes.Buffer usabile da più goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
