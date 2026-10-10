package alert

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Message è il testo stampato e registrato quando viene rilevata una parola
// chiave. Il primo segnaposto riceve la parola che ha fatto scattare
// l'allarme, così la riga dice sempre quale parola è stata udita e non una
// parola fissa; il secondo la telecamera che l'ha sentita.
const Message = "%s rilevato da %s, luce accesa"

// Cooldown sopprime gli allarmi ripetuti per la stessa parola chiave, sulla
// stessa telecamera, entro una finestra temporale.
//
// Serve perché Vosk emette una trascrizione ogni 100 ms: mentre la persona sta
// ancora parlando, la parola resta dentro l'ipotesi parziale e farebbe
// scattare l'allarme una volta per ogni blocco di audio. Senza questo filtro un
// solo "aiuto" produrrebbe una raffica di righe identiche e il segnale reale
// andrebbe perso nel rumore.
//
// Il filtro è per telecamera: un "aiuto" sentito dall'ingresso non deve
// zittire lo stesso "aiuto" sentito in cortile un secondo dopo.
type Cooldown struct {
	window time.Duration

	mu   sync.Mutex
	last map[cooldownKey]time.Time
}

type cooldownKey struct {
	camera, keyword string
}

// NewCooldown restituisce un Cooldown con la finestra indicata. Una finestra
// nulla o negativa disattiva la soppressione e lascia passare ogni match,
// come nel comportamento naif.
func NewCooldown(window time.Duration) *Cooldown {
	return &Cooldown{
		window: window,
		last:   make(map[cooldownKey]time.Time),
	}
}

// Allow indica se per la parola chiave keyword sentita dalla telecamera
// camera all'istante now deve essere emesso un allarme. Restituisce false
// quando la stessa coppia ha già fatto scattare l'allarme meno di window fa, e
// in caso contrario registra il nuovo istante.
func (c *Cooldown) Allow(camera, keyword string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.window <= 0 {
		return true
	}
	key := cooldownKey{camera: camera, keyword: keyword}
	if prev, ok := c.last[key]; ok && now.Sub(prev) < c.window {
		return false
	}
	c.last[key] = now
	return true
}

// Event descrive una parola chiave rilevata.
//
// contiene solo la parola chiave, mai la frase in cui è stata trovata: è
// l'unica forma in cui una parola pronunciata può finire su disco, e quella
// deve essere sempre una delle parole chiave configurate. Le altre parole
// restano in memoria, per il confronto, e non vengono registrate.
type Event struct {
	Keyword string
	// Camera è il nome configurato della telecamera che ha sentito la
	// parola chiave.
	Camera string
	// Host è l'indirizzo IP (o il nome) della telecamera, senza credenziali.
	Host string
	At   time.Time
}

// source descrive la telecamera per la riga di allarme: nome e indirizzo.
func (e Event) source() string {
	if e.Host == "" || e.Host == e.Camera {
		return e.Camera
	}
	return e.Camera + " (" + e.Host + ")"
}

// Alerter reagisce a una parola chiave rilevata.
//
// La luce con GPIO verrà aggiunta in futuro come altra implementazione di
// questa interfaccia: il resto del programma non dovrà cambiare.
type Alerter interface {
	// Name identifica l'alerter nei log.
	Name() string
	// Trigger viene chiamata per ogni parola chiave rilevata.
	Trigger(Event) error
}

// TerminalAlerter stampa l'evento sul terminale e lo scrive nel file di log
// con un indicatore di tempo.
type TerminalAlerter struct {
	out  io.Writer
	log  *slog.Logger
	mu   sync.Mutex
	now  func() time.Time
	name string
}

// NewTerminal costruisce un Alerter che scrive su out e registra su log.
func NewTerminal(out io.Writer, log *slog.Logger) *TerminalAlerter {
	return &TerminalAlerter{out: out, log: log, now: time.Now, name: "terminal"}
}

// Name implementa Alerter.
func (t *TerminalAlerter) Name() string { return t.name }

// Trigger implementa Alerter: stampa la parola chiave rilevata sul terminale e
// accoda una riga con l'orario nel file di log. Sul terminale non compare
// nient'altro, così chi guarda la console vede subito l'emergenza.
func (t *TerminalAlerter) Trigger(e Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	ts := e.At
	if ts.IsZero() {
		ts = t.now()
	}
	msg := fmt.Sprintf(Message, e.Keyword, e.source())

	if _, err := fmt.Fprintf(t.out, "%s %s\n", ts.Format("15:04:05"), msg); err != nil {
		return fmt.Errorf("alert: scrittura su terminale fallita: %w", err)
	}

	// Nessun altro testo oltre alla parola chiave e alla telecamera: vedi
	// la nota su Event.
	t.log.Info(msg,
		slog.Time("timestamp", ts),
		slog.String("keyword", e.Keyword),
		slog.String("camera", e.Camera),
		slog.String("camera_ip", e.Host),
	)
	return nil
}
