package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("scrittura config di test: %v", err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	path := writeConfig(t, `{
		"keywords": ["Aiuto", " soccorso ", "aiuto"],
		"model_path": "m",
		"log_file": "l.log"
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// "Aiuto" e "aiuto" sono duplicati una volta normalizzati, " soccorso "
	// ha spazi attorno.
	if len(cfg.Keywords) != 2 {
		t.Errorf("keywords = %v, attese 2 dopo normalizzazione", cfg.Keywords)
	}
	if cfg.Keywords[0] != "aiuto" || cfg.Keywords[1] != "soccorso" {
		t.Errorf("keywords = %v, attese [aiuto soccorso]", cfg.Keywords)
	}
	if cfg.SampleRate != 16000 {
		t.Errorf("sample_rate = %d, atteso 16000", cfg.SampleRate)
	}
	if cfg.DeviceID != -1 {
		t.Errorf("device_id = %d, atteso -1 (predefinito di sistema)", cfg.DeviceID)
	}
	if cfg.Cooldown() != 3*time.Second {
		t.Errorf("cooldown = %v, atteso 3s come default", cfg.Cooldown())
	}

	// I percorsi relativi sono risolti rispetto alla directory della config.
	want := filepath.Join(filepath.Dir(path), "m")
	if cfg.AbsModelPath() != want {
		t.Errorf("model path = %q, atteso %q", cfg.AbsModelPath(), want)
	}
	wantAlerts := filepath.Join(filepath.Dir(path), "logs/help-alarm-alerts.log")
	if cfg.AbsAlertLogPath() != wantAlerts {
		t.Errorf("alert log path = %q, atteso %q", cfg.AbsAlertLogPath(), wantAlerts)
	}
}

func TestCooldownIsConfigurable(t *testing.T) {
	path := writeConfig(t, `{"keywords":["aiuto"],"cooldown_seconds":0}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 0 disattiva il filtro: ogni match deve far scattare l'allarme.
	if cfg.Cooldown() != 0 {
		t.Errorf("cooldown = %v, atteso 0", cfg.Cooldown())
	}

	path = writeConfig(t, `{"keywords":["aiuto"],"cooldown_seconds":0.5}`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cooldown() != 500*time.Millisecond {
		t.Errorf("cooldown = %v, atteso 500ms", cfg.Cooldown())
	}
}

func TestAlertLogFileIsOptional(t *testing.T) {
	path := writeConfig(t, `{"keywords":["aiuto"],"alert_log_file":""}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AbsAlertLogPath() != "" {
		t.Errorf("alert log path = %q, attesa stringa vuota", cfg.AbsAlertLogPath())
	}
}

func TestLoadAbsolutePathsAreKept(t *testing.T) {
	path := writeConfig(t, `{"keywords":["aiuto"],"model_path":"/opt/vosk","log_file":"/var/log/a.log"}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AbsModelPath() != "/opt/vosk" {
		t.Errorf("model path = %q, atteso /opt/vosk", cfg.AbsModelPath())
	}
	if cfg.AbsLogPath() != "/var/log/a.log" {
		t.Errorf("log path = %q, atteso /var/log/a.log", cfg.AbsLogPath())
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"nessuna keyword", `{"keywords":[],"model_path":"m","log_file":"l"}`},
		{"solo spazi nelle keyword", `{"keywords":["  "],"model_path":"m","log_file":"l"}`},
		{"sample rate non supportata", `{"keywords":["aiuto"],"model_path":"m","log_file":"l","sample_rate":44100}`},
		{"livello di log ignoto", `{"keywords":["aiuto"],"model_path":"m","log_file":"l","log_level":"verboso"}`},
		{"model path vuoto", `{"keywords":["aiuto"],"model_path":"  ","log_file":"l"}`},
		{"log file vuoto", `{"keywords":["aiuto"],"model_path":"m","log_file":""}`},
		{"cooldown negativo", `{"keywords":["aiuto"],"model_path":"m","log_file":"l","cooldown_seconds":-1}`},
		{"json malformato", `{"keywords":`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.body)); err == nil {
				t.Error("Load accettato una configurazione non valida")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "inesistente.json")); err == nil {
		t.Error("Load accettato un file inesistente")
	}
}
