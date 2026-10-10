package stt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	vosk "github.com/alphacep/vosk-api/go"
)

// Phrase è una trascrizione emessa dal riconoscitore.
type Phrase struct {
	Text string
	// Partial è vera quando il testo è solo l'ipotesi corrente e può quindi
	// cambiare con l'audio successivo.
	Partial bool
}

// Result è la parte del JSON di Vosk che ci interessa.
// Un risultato definitivo ha la forma {"text": "..."} mentre un'ipotesi
// parziale ha la forma {"partial": "..."}: in entrambi i casi la trascrizione
// è una stringa semplice.
type Result struct {
	Text    string `json:"text"`
	Partial string `json:"partial"`
}

// Model è il modello acustico Vosk. Si carica una volta sola ed è condiviso
// da tutti i Recognizer, uno per telecamera: è la parte pesante in memoria,
// e Vosk ne permette l'uso da più recognizer in parallelo.
type Model struct {
	model *vosk.VoskModel
}

// LoadModel carica il modello Vosk situato in modelPath. La cartella deve
// contenere i file prodotti dagli archivi vosk-model-small-it-* (am/, conf/,
// graph/).
func LoadModel(modelPath string) (*Model, error) {
	if err := checkModel(modelPath); err != nil {
		return nil, err
	}

	// -1 silenzia il Verboso nativo di Vosk, che altrimenti scrive
	// direttamente su stdout e mescola il proprio output con il nostro.
	vosk.SetLogLevel(-1)

	model, err := vosk.NewModel(modelPath)
	if err != nil {
		return nil, fmt.Errorf("stt: caricamento modello Vosk fallito: %w", err)
	}
	return &Model{model: model}, nil
}

// Close libera il modello. Va chiamata dopo aver chiuso ogni Recognizer che
// lo usa. Si può chiamare più di una volta.
func (m *Model) Close() {
	if m.model != nil {
		m.model.Free()
		m.model = nil
	}
}

// Recognizer trasforma PCM grezzi in Phrase usando un Model condiviso.
// Non è sicuro per l'uso concorrente: ne possiede una sola goroutine, e ogni
// telecamera ha il proprio, perché tiene lo stato della frase in corso.
type Recognizer struct {
	rec *vosk.VoskRecognizer
}

// NewRecognizer crea un riconoscitore a 16 kHz sul modello indicato.
func NewRecognizer(m *Model) (*Recognizer, error) {
	rec, err := vosk.NewRecognizer(m.model, 16000.0)
	if err != nil {
		return nil, fmt.Errorf("stt: creazione recognizer fallita: %w", err)
	}
	return &Recognizer{rec: rec}, nil
}

func checkModel(modelPath string) error {
	info, err := os.Stat(modelPath)
	if err != nil {
		return fmt.Errorf("stt: modello Vosk non trovato in %s: %w", modelPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("stt: %s non è una directory", modelPath)
	}
	if _, err := os.Stat(filepath.Join(modelPath, "am", "final.mdl")); err != nil {
		return fmt.Errorf("stt: %s non è un modello Vosk valido (manca am/final.mdl): %w", modelPath, err)
	}
	return nil
}

// Accept passa un blocco di PCM (s16, mono, 16 kHz) al riconoscitore.
//
// Quando final è vera Vosk ha rilevato la fine di un enunciato e Phrase.Text
// è la trascrizione completa. Altrimenti la Phrase restituita è l'ipotesi
// corrente, che le chiamate successive possono ancora correggere.
func (r *Recognizer) Accept(pcm []byte) (Phrase, bool, error) {
	if r.rec.AcceptWaveform(pcm) != 0 {
		text, err := parseText(r.rec.Result())
		if err != nil {
			return Phrase{}, false, err
		}
		return Phrase{Text: text}, true, nil
	}

	text, err := parseText(r.rec.PartialResult())
	if err != nil {
		return Phrase{}, false, err
	}
	return Phrase{Text: text, Partial: true}, false, nil
}

// Flush chiede a Vosk di emettere l'enunciato ancora in corso senza aspettare
// il silenzio. Restituisce una Phrase vuota quando non c'era nulla in sospeso.
func (r *Recognizer) Flush() (Phrase, error) {
	if r.rec == nil {
		return Phrase{}, nil
	}
	text, err := parseText(r.rec.FinalResult())
	if err != nil {
		return Phrase{}, err
	}
	return Phrase{Text: text}, nil
}

// Close libera le risorse native del riconoscitore, non il modello. Si può
// chiamare più di una volta.
func (r *Recognizer) Close() {
	if r.rec != nil {
		r.rec.Free()
		r.rec = nil
	}
}

// parseText estrae la trascrizione da un JSON di Vosk, che sia un risultato
// definitivo ({"text": ...}) sia un'ipotesi parziale ({"partial": ...}).
func parseText(payload string) (string, error) {
	var res Result
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		return "", fmt.Errorf("stt: JSON Vosk non valido %q: %w", payload, err)
	}
	if res.Text != "" {
		return res.Text, nil
	}
	return res.Partial, nil
}
