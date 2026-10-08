package keyword

import (
	"strings"
	"unicode"
)

// Match è una singola occorrenza di parola chiave trovata in una trascrizione.
type Match struct {
	// Keyword è la parola chiave esattamente come scritta nella configurazione.
	Keyword string
	// Phrase è la trascrizione in cui la parola chiave è stata trovata. Serve
	// solo per il diagnostico in modalità dettagliata: non viene mai registrata.
	Phrase string
}

type entry struct {
	original string
	tokens   []string
}

// Matcher cerca nelle trascrizioni le parole chiave configurate.
//
// Una parola chiave scatta solo a confine di parola, quindi "aiuto" non
// scatta su "aiutone" né su "inaiuto". Le parole chiave che contengono
// spazi sono cercate come sequenze di parole consecutive.
type Matcher struct {
	// byLength raggruppa le parole chiave per numero di parole, così una
	// sola passata sui token basta e le sequenze più lunghe vengono provate
	// per prime.
	byLength map[int][]entry
	maxLen   int
}

// New costruisce un Matcher per le parole chiave indicate. Le parole chiave
// vengono normalizzate come le trascrizioni, quindi il confronto non distingue
// maiuscole, minuscole e punteggiatura.
func New(keywords []string) *Matcher {
	m := &Matcher{byLength: make(map[int][]entry)}
	for _, k := range keywords {
		tokens := tokenize(k)
		if len(tokens) == 0 {
			continue
		}
		n := len(tokens)
		m.byLength[n] = append(m.byLength[n], entry{original: k, tokens: tokens})
		if n > m.maxLen {
			m.maxLen = n
		}
	}
	return m
}

// Empty indica se il matcher non ha nessuna parola chiave utilizzabile.
func (m *Matcher) Empty() bool { return m.maxLen == 0 }

// Find restituisce ogni occorrenza di parola chiave in phrase, nell'ordine in
// cui compare.
func (m *Matcher) Find(phrase string) []Match {
	if m.Empty() {
		return nil
	}

	tokens := tokenize(phrase)
	var matches []Match

	for i := 0; i < len(tokens); i++ {
		max := m.maxLen
		if i+max > len(tokens) {
			max = len(tokens) - i
		}
		for n := max; n >= 1; n-- {
			window := tokens[i : i+n]
			for _, e := range m.byLength[n] {
				if equalTokens(window, e.tokens) {
					matches = append(matches, Match{Keyword: e.original, Phrase: phrase})
				}
			}
		}
	}
	return matches
}

// tokenize mette in minuscolo il testo, trasforma ogni carattere non
// alfanumerico in uno spazio e divide il risultato in parole.
func tokenize(s string) []string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

func equalTokens(window, keyword []string) bool {
	for i := range keyword {
		if window[i] != keyword[i] {
			return false
		}
	}
	return true
}
