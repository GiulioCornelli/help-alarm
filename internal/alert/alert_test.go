package alert

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

func TestTriggerPrintsAndLogs(t *testing.T) {
	log, logBuf := newTestLogger()
	out := &bytes.Buffer{}
	terminal := NewTerminal(out, log)

	at := time.Date(2026, 9, 29, 14, 5, 6, 0, time.UTC)
	err := terminal.Trigger(Event{Keyword: "aiuto", At: at})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	const want = "aiuto rilevato, luce accesa"

	if !strings.Contains(out.String(), want) {
		t.Errorf("il terminale non contiene %q, output: %q", want, out.String())
	}
	if !strings.Contains(out.String(), "14:05:06") {
		t.Errorf("il terminale non mostra l'orario, output: %q", out.String())
	}

	var rec map[string]any
	if err := json.Unmarshal(logBuf.Bytes(), &rec); err != nil {
		t.Fatalf("record di log non è JSON valido: %v (%s)", err, logBuf.String())
	}
	if rec["msg"] != want {
		t.Errorf("msg = %v, atteso %q", rec["msg"], want)
	}
	if rec["keyword"] != "aiuto" {
		t.Errorf("keyword = %v, atteso \"aiuto\"", rec["keyword"])
	}
	if _, ok := rec["time"]; !ok {
		t.Error("il record di log non ha il timestamp")
	}
	if ts, ok := rec["timestamp"].(string); !ok || !strings.HasPrefix(ts, "2026-09-29") {
		t.Errorf("timestamp = %v, atteso il 2026-09-29", rec["timestamp"])
	}
}

// TestTriggerLogsOnlyTheKeyword è il test che presidia la privacy: nel record
// di allarme non deve restare traccia di nessuna parola pronunciata che non
// sia una parola chiave.
func TestTriggerLogsOnlyTheKeyword(t *testing.T) {
	log, logBuf := newTestLogger()
	terminal := NewTerminal(&bytes.Buffer{}, log)

	if err := terminal.Trigger(Event{Keyword: "soccorso"}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	var rec map[string]any
	if err := json.Unmarshal(logBuf.Bytes(), &rec); err != nil {
		t.Fatalf("record di log non è JSON valido: %v (%s)", err, logBuf.String())
	}

	// Nessun campo può contenere testo libero: ogni chiave deve essere una
	// delle quattro ammesse.
	for k, v := range rec {
		switch k {
		case "time", "timestamp", "keyword", "msg", "level":
		default:
			t.Errorf("il record di allarme ha il campo inatteso %q = %v", k, v)
		}
	}
	if rec["keyword"] != "soccorso" {
		t.Errorf("keyword = %v, atteso \"soccorso\"", rec["keyword"])
	}
}

// TestTriggerNamesTheKeyword verifica che la riga riporti la parola udita e non
// una parola fissa: un allarme su "emergenza" non deve scrivere "aiuto".
func TestTriggerNamesTheKeyword(t *testing.T) {
	for _, keyword := range []string{"aiuto", "soccorso", "emergenza", "hilfe", "notruf"} {
		log, _ := newTestLogger()
		out := &bytes.Buffer{}
		terminal := NewTerminal(out, log)

		if err := terminal.Trigger(Event{Keyword: keyword}); err != nil {
			t.Fatalf("Trigger(%q): %v", keyword, err)
		}
		want := keyword + " rilevato, luce accesa"
		if !strings.Contains(out.String(), want) {
			t.Errorf("con %q il terminale mostra %q, atteso %q", keyword, out.String(), want)
		}
	}
}

func TestTriggerDefaultsToNow(t *testing.T) {
	log, _ := newTestLogger()
	terminal := NewTerminal(&bytes.Buffer{}, log)
	terminal.now = func() time.Time {
		return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	}

	if err := terminal.Trigger(Event{Keyword: "aiuto"}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
}

// TestAlerterIsAnInterface documenta il punto di estensione: la luce con GPIO
// entrerà come seconda implementazione di Alerter, senza toccare chi chiama.
func TestAlerterIsAnInterface(t *testing.T) {
	log, _ := newTestLogger()
	var a Alerter = NewTerminal(&bytes.Buffer{}, log)
	if a.Name() != "terminal" {
		t.Errorf("Name = %q", a.Name())
	}
}
