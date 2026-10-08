package stt

import "testing"

func TestParseTextFinal(t *testing.T) {
	got, err := parseText(`{"text" : "per favore aiuto"}`)
	if err != nil {
		t.Fatalf("parseText: %v", err)
	}
	if got != "per favore aiuto" {
		t.Errorf("parseText = %q", got)
	}
}

func TestParseTextPartial(t *testing.T) {
	// Vosk restituisce "partial" come stringa piana, non come oggetto.
	got, err := parseText(`{"partial" : "per favore"}`)
	if err != nil {
		t.Fatalf("parseText: %v", err)
	}
	if got != "per favore" {
		t.Errorf("parseText = %q", got)
	}
}

func TestParseTextEmpty(t *testing.T) {
	for _, payload := range []string{`{"text" : ""}`, `{"partial" : ""}`, `{}`} {
		got, err := parseText(payload)
		if err != nil {
			t.Errorf("parseText(%s): %v", payload, err)
		}
		if got != "" {
			t.Errorf("parseText(%s) = %q, attesa stringa vuota", payload, got)
		}
	}
}

func TestParseTextInvalid(t *testing.T) {
	if _, err := parseText(`{"text" : 42}`); err == nil {
		t.Error("parseText ha accettato un JSON con tipo sbagliato")
	}
}

func TestOpenMissingModel(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("Open ha accettato una directory senza modello")
	}
}
