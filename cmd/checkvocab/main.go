package main

import (
	"fmt"
	"os"
	"strings"

	vosk "github.com/alphacep/vosk-api/go"
)

func main() {
	vosk.SetLogLevel(-1)
	m, err := vosk.NewModel("models/vosk-model-small-it-0.22")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer m.Free()

	groups := []struct {
		label string
		words []string
	}{
		{"italiano (riferimento)", []string{"aiuto", "aiutami", "soccorso", "emergenza", "sos"}},
		{"inglese", []string{"help", "emergency", "danger", "alarm", "please", "need"}},
		{"francese", []string{"secours", "aide", "aidez", "urgence", "sauve", "svp"}},
		{"tedesco", []string{"hilfe", "notfall", "notruf", "feuer", "rettung", "danke"}},
	}

	for _, g := range groups {
		var presenti, assenti []string
		for _, w := range g.words {
			if m.FindWord(w) >= 0 {
				presenti = append(presenti, w)
			} else {
				assenti = append(assenti, w)
			}
		}
		fmt.Printf("%s\n  nel vocabolario: %s\n  assenti:         %s\n\n",
			strings.ToUpper(g.label),
			join(presenti), join(assenti))
	}
}

func join(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ", ")
}
