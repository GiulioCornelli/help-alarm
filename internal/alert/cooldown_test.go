package alert

import (
	"testing"
	"time"
)

var base = time.Date(2026, 9, 29, 16, 10, 20, 0, time.UTC)

func TestCooldownSuppressesBurst(t *testing.T) {
	c := NewCooldown(3 * time.Second)

	// Vosk rilancia la parola ogni 100 ms finché la persona parla: ci
	// aspettiamo un solo allarme.
	fired := 0
	for i := 0; i < 10; i++ {
		if c.Allow("aiuto", base.Add(time.Duration(i)*100*time.Millisecond)) {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("10 match ravvicinati hanno prodotto %d allarmi, atteso 1", fired)
	}
}

// TestCooldownAllowsRepeatedUtterance verifica che gridare "aiuto" due volte di
// seguito a distanza di voce umana produca comunque due allarmi.
func TestCooldownAllowsRepeatedUtterance(t *testing.T) {
	c := NewCooldown(3 * time.Second)

	if !c.Allow("aiuto", base) {
		t.Fatal("il primo match deve passare")
	}
	if c.Allow("aiuto", base.Add(2*time.Second)) {
		t.Error("un match entro la finestra deve essere soppresso")
	}
	if !c.Allow("aiuto", base.Add(4*time.Second)) {
		t.Error("un match dopo la finestra deve passare")
	}
}

func TestCooldownIsPerKeyword(t *testing.T) {
	c := NewCooldown(3 * time.Second)

	if !c.Allow("aiuto", base) {
		t.Fatal("il primo match deve passare")
	}
	// Una parola chiave diversa non deve essere bloccata da "aiuto".
	if !c.Allow("soccorso", base.Add(time.Second)) {
		t.Error("una parola chiave diversa deve passare comunque")
	}
}

func TestCooldownZeroDisables(t *testing.T) {
	for _, window := range []time.Duration{0, -time.Second} {
		c := NewCooldown(window)
		for i := 0; i < 5; i++ {
			if !c.Allow("aiuto", base) {
				t.Fatalf("con cooldown %v ogni match deve passare", window)
			}
		}
	}
}

func TestCooldownDoesNotDrift(t *testing.T) {
	// Il tempo fornito è sempre monotono: Allow non deve usare time.Now().
	c := NewCooldown(time.Second)
	if !c.Allow("aiuto", base) {
		t.Fatal("il primo match deve passare")
	}
	if !c.Allow("aiuto", base.Add(1500*time.Millisecond)) {
		t.Error("match dopo 1.5s con cooldown 1s deve passare")
	}
}
