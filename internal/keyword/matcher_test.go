package keyword

import "testing"

func TestFindWholeWordsOnly(t *testing.T) {
	m := New([]string{"aiuto"})

	cases := []struct {
		phrase string
		want   int
	}{
		{"aiuto", 1},
		{"Aiuto!", 1},
		{"  AIUTO  ", 1},
		{"per favore aiuto", 1},
		{"aiutami", 0},
		{"inaiuto", 0},
		{"aiutone", 0},
		{"nessuna parola qui", 0},
		{"", 0},
		{"aiuto, per favore. aiuto!", 2},
	}

	for _, tc := range cases {
		got := len(m.Find(tc.phrase))
		if got != tc.want {
			t.Errorf("Find(%q) = %d match, attesi %d", tc.phrase, got, tc.want)
		}
	}
}

func TestFindMultipleKeywords(t *testing.T) {
	m := New([]string{"aiuto", "soccorso", "emergenza"})

	got := m.Find("per favore soccorso, è un'emergenza")
	if len(got) != 2 {
		t.Fatalf("attesi 2 match, ottenuti %d: %+v", len(got), got)
	}
	if got[0].Keyword != "soccorso" || got[1].Keyword != "emergenza" {
		t.Errorf("match nell'ordine sbagliato: %+v", got)
	}
}

func TestFindMultiWordKeyword(t *testing.T) {
	m := New([]string{"per favore aiuto"})

	if got := m.Find("per favore aiuto ora"); len(got) != 1 {
		t.Errorf("atteso 1 match, ottenuti %d", len(got))
	}
	// Le parole devono essere consecutive.
	if got := m.Find("per aiuto favore"); len(got) != 0 {
		t.Errorf("attesi 0 match su parole non consecutive, ottenuti %d", len(got))
	}
	// Una singola parola non deve far scattare la frase.
	mSingle := New([]string{"aiuto"})
	if got := mSingle.Find("per favore aiuto"); len(got) != 1 {
		t.Errorf("atteso 1 match per parola singola, ottenuti %d", len(got))
	}
}

func TestMatchReportsOriginalSpelling(t *testing.T) {
	m := New([]string{"Aiuto!"})
	got := m.Find("aiuto per favore")
	if len(got) != 1 {
		t.Fatalf("atteso 1 match, ottenuti %d", len(got))
	}
	if got[0].Keyword != "Aiuto!" {
		t.Errorf("Keyword = %q, attesa la grafia della configurazione", got[0].Keyword)
	}
	if got[0].Phrase != "aiuto per favore" {
		t.Errorf("Phrase = %q, attesa la trascrizione originale", got[0].Phrase)
	}
}

func TestNewIgnoresEmptyAndPunctuationOnly(t *testing.T) {
	m := New([]string{"", "   ", "---", "aiuto"})
	if m.Empty() {
		t.Fatal("il matcher dovrebbe avere almeno una parola utile")
	}
	if got := len(m.Find("aiuto")); got != 1 {
		t.Errorf("atteso 1 match, ottenuti %d", got)
	}
}

func TestEmptyMatcher(t *testing.T) {
	m := New(nil)
	if !m.Empty() {
		t.Error("un matcher senza keyword deve essere vuoto")
	}
	if got := m.Find("aiuto"); got != nil {
		t.Errorf("un matcher vuoto non deve restituire match, ottenuti %+v", got)
	}
}

func TestTokenizeStripsPunctuationAndDigits(t *testing.T) {
	m := New([]string{"aiuto 123"})
	if got := len(m.Find("Aiuto, 123!")); got != 1 {
		t.Errorf("atteso 1 match su \"Aiuto, 123!\", ottenuti %d", got)
	}
}
