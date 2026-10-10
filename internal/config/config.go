package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config raccoglie ogni parametro regolabile dell'applicazione.
type Config struct {
	// Keywords da cercare nella trascrizione. Sono supportate sia le singole
	// parole sia le frasi di più parole.
	Keywords []string `json:"keywords"`
	// ModelPath è la cartella del modello acustico Vosk.
	ModelPath string `json:"model_path"`
	// Cameras sono le telecamere IP da ascoltare, ognuna con la propria
	// catena di trascrizione.
	Cameras []Camera `json:"cameras"`
	// SampleRate in Hz. Vosk richiede 16000.
	SampleRate int `json:"sample_rate"`
	// LogFile è il percorso del file di log strutturato con i messaggi
	// ordinari.
	LogFile string `json:"log_file"`
	// AlertLogFile è il percorso del file che riceve i soli record di
	// allarme. Se è vuoto gli allarmi finiscono in LogFile.
	AlertLogFile string `json:"alert_log_file"`
	// LogLevel è uno tra: debug, info, warn, error.
	LogLevel string `json:"log_level"`
	// LogTranscriptions abilita la registrazione di ogni frase riconosciuta.
	LogTranscriptions bool `json:"log_transcriptions"`
	// CooldownSeconds è il numero minimo di secondi fra due allarmi della
	// stessa parola chiave. Zero significa nessuna soppressione: ogni match
	// fa scattare l'allarme.
	CooldownSeconds float64 `json:"cooldown_seconds"`

	// dir è la cartella che contiene il file di configurazione, usata per
	// risolvere i percorsi relativi di ModelPath e LogFile.
	dir string
}

// Camera è una telecamera IP di cui ascoltare l'audio.
type Camera struct {
	// Name identifica la telecamera nei log e negli allarmi. Deve essere
	// unico.
	Name string `json:"name"`
	// URL è l'indirizzo RTSP dello stream, con le eventuali credenziali,
	// per esempio rtsp://utente:password@192.168.1.10:554/stream1. Non viene
	// mai scritto nei log: compaiono solo Name e l'host.
	URL string `json:"url"`
}

const voskSampleRate = 16000

// Load legge e valida la configurazione contenuta nel file path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lettura config: %w", err)
	}

	cfg := &Config{
		ModelPath:         "models/vosk-model-small-it-0.22",
		SampleRate:        voskSampleRate,
		LogFile:           "logs/help-alarm.log",
		AlertLogFile:      "logs/help-alarm-alerts.log",
		LogLevel:          "info",
		LogTranscriptions: true,
		CooldownSeconds:   3,
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config non valido %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("percorso config: %w", err)
	}
	cfg.dir = filepath.Dir(abs)

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	keywords := make([]string, 0, len(c.Keywords))
	seen := make(map[string]struct{}, len(c.Keywords))
	for _, k := range c.Keywords {
		k = strings.TrimSpace(strings.ToLower(k))
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keywords = append(keywords, k)
	}
	if len(keywords) == 0 {
		return fmt.Errorf("config non valido: \"keywords\" deve contenere almeno una parola chiave")
	}
	c.Keywords = keywords

	if err := c.normalizeCameras(); err != nil {
		return err
	}

	if c.SampleRate != voskSampleRate {
		return fmt.Errorf("config non valido: sample_rate deve essere %d (richiesto da Vosk), trovato %d", voskSampleRate, c.SampleRate)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config non valido: log_level %q non ammesso (debug, info, warn, error)", c.LogLevel)
	}

	if strings.TrimSpace(c.ModelPath) == "" {
		return fmt.Errorf("config non valido: \"model_path\" è obbligatorio")
	}
	if strings.TrimSpace(c.LogFile) == "" {
		return fmt.Errorf("config non valido: \"log_file\" è obbligatorio")
	}
	if c.CooldownSeconds < 0 {
		return fmt.Errorf("config non valido: cooldown_seconds non può essere negativo, trovato %v", c.CooldownSeconds)
	}

	c.ModelPath = c.resolve(c.ModelPath)
	c.LogFile = c.resolve(c.LogFile)
	c.AlertLogFile = strings.TrimSpace(c.AlertLogFile)
	if c.AlertLogFile != "" {
		c.AlertLogFile = c.resolve(c.AlertLogFile)
	}
	return nil
}

// normalizeCameras verifica l'elenco delle telecamere. I messaggi di errore
// citano la telecamera per nome o posizione, mai per URL: l'URL può
// contenere la password.
func (c *Config) normalizeCameras() error {
	if len(c.Cameras) == 0 {
		return fmt.Errorf("config non valido: \"cameras\" deve contenere almeno una telecamera")
	}
	seen := make(map[string]struct{}, len(c.Cameras))
	for i := range c.Cameras {
		cam := &c.Cameras[i]
		cam.Name = strings.TrimSpace(cam.Name)
		cam.URL = strings.TrimSpace(cam.URL)
		if cam.Name == "" {
			return fmt.Errorf("config non valido: la telecamera n. %d non ha \"name\"", i+1)
		}
		if _, dup := seen[cam.Name]; dup {
			return fmt.Errorf("config non valido: il nome di telecamera %q è usato più volte", cam.Name)
		}
		seen[cam.Name] = struct{}{}

		u, err := url.Parse(cam.URL)
		if err != nil || cam.URL == "" {
			return fmt.Errorf("config non valido: telecamera %q, \"url\" mancante o non valido", cam.Name)
		}
		if u.Scheme != "rtsp" && u.Scheme != "rtsps" {
			return fmt.Errorf("config non valido: telecamera %q, \"url\" deve iniziare con rtsp:// o rtsps://", cam.Name)
		}
		if u.Hostname() == "" {
			return fmt.Errorf("config non valido: telecamera %q, \"url\" senza indirizzo", cam.Name)
		}
	}
	return nil
}

// resolve trasforma un percorso eventualmente relativo in uno assoluto,
// relativo alla cartella che contiene il file di configurazione.
func (c *Config) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(c.dir, p))
}

// AbsModelPath restituisce il percorso assoluto della cartella del modello Vosk.
func (c *Config) AbsModelPath() string { return c.ModelPath }

// AbsLogPath restituisce il percorso assoluto del file di log dei messaggi
// ordinari.
func (c *Config) AbsLogPath() string { return c.LogFile }

// AbsAlertLogPath restituisce il percorso assoluto del file che riceve i soli
// allarmi. È vuoto quando il chiamante ha chiesto che finiscano nel log
// ordinario.
func (c *Config) AbsAlertLogPath() string { return c.AlertLogFile }

// Cooldown restituisce il tempo minimo fra due allarmi della stessa parola chiave.
func (c *Config) Cooldown() time.Duration {
	return time.Duration(c.CooldownSeconds * float64(time.Second))
}
