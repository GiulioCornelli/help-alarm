package audio

import (
	"fmt"
	"io"
	"sync"

	"github.com/gen2brain/malgo"
)

// Config descrive quale dispositivo di acquisizione aprire e in quale formato PCM	.
type Config struct {
	// DeviceID indica il dispositivo di cattura. Un valore negativo significa
	// "quello predefinito di sistema"; altrimenti è l'indice restituito da
	// ListDevices.
	DeviceID int
	// SampleRate in Hz. Vosk richiede 16000.
	SampleRate uint32
}

// Capture trasmette in streaming blocchi di PCM grezzi da un microfono.
//
// I blocchi sono prodotti da miniaudio sul proprio thread audio: la callback
// copia soltanto il buffer e lo consegna a un canale, non blocca mai, così il
// thread audio non viene mai fermato dalla catena di elaborazione a valle.
type Capture struct {
	cfg    Config
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	name   string

	ch   chan []byte
	once sync.Once
}

// backendsForPlatform elenca in ordine i backend audio da provare. ALSA viene
// per primo, così una Linux senza sound server funziona comunque.
func backendsForPlatform() []malgo.Backend {
	return []malgo.Backend{malgo.BackendAlsa, malgo.BackendPulseaudio}
}

// ListDevices scrive su w i dispositivi di cattura disponibili, con l'indice
// da mettere in "device_id" nel file di configurazione.
func ListDevices(w io.Writer) error {
	backends := backendsForPlatform()
	allocCtx, err := malgo.InitContext(backends, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return fmt.Errorf("audio: inizializzazione contesto fallita: %w", err)
	}
	defer allocCtx.Free()

	devices, err := allocCtx.Devices(malgo.Capture)
	if err != nil {
		return fmt.Errorf("audio: elenco dispositivi fallito: %w", err)
	}

	if len(devices) == 0 {
		fmt.Fprintln(w, "nessun microfono trovato")
		return nil
	}

	fmt.Fprintln(w, "dispositivi di cattura (indice da usare in config.json -> device_id):")
	for i, d := range devices {
		marker := " "
		if d.IsDefault != 0 {
			marker = "*"
		}
		fmt.Fprintf(w, " %s %d  %s  [%s]\n", marker, i, d.Name(), d.ID.String())
	}
	fmt.Fprintln(w, "  * = dispositivo predefinito di sistema (corrisponde a device_id: -1)")
	return nil
}

// New apre il dispositivo di cattura ma non avvia ancora lo streaming.
func New(cfg Config) (*Capture, error) {
	if cfg.SampleRate == 0 {
		return nil, fmt.Errorf("audio: sample rate mancante")
	}

	backends := backendsForPlatform()
	allocCtx, err := malgo.InitContext(backends, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("audio: inizializzazione contesto fallita: %w", err)
	}

	c := &Capture{
		cfg: cfg,
		ctx: allocCtx,
		// 8 slot: abbastanza margine per non perdere frame se la
		// trascrizione rallenta per un istante.
		ch: make(chan []byte, 8),
	}

	deviceCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	// Senza questo, miniaudio apre il dispositivo alla sua frequenza nativa
	// (spesso 32000 o 48000 Hz) e le trasformiamo in silenzio.
	deviceCfg.SampleRate = cfg.SampleRate
	deviceCfg.PeriodSizeInMilliseconds = 100
	deviceCfg.Periods = 3
	// Vosk accetta solo PCM s16 little endian, mono, 16 kHz: miniaudio
	// converte per noi se il microfono espone altro.
	deviceCfg.Capture.Format = malgo.FormatS16
	deviceCfg.Capture.Channels = 1
	deviceCfg.Capture.ShareMode = malgo.Shared
	// Speex non è compilato in miniaudio, il resampling lineare sì.
	deviceCfg.Resampling = malgo.ResampleConfig{Algorithm: malgo.ResampleAlgorithmLinear}

	if err := c.applyDeviceID(&deviceCfg); err != nil {
		allocCtx.Free()
		return nil, err
	}

	device, err := malgo.InitDevice(allocCtx.Context, deviceCfg, malgo.DeviceCallbacks{Data: c.onData})
	if err != nil {
		allocCtx.Free()
		return nil, fmt.Errorf("audio: apertura microfono fallita: %w", err)
	}
	c.device = device
	c.name = c.resolveName()

	if err := c.verifyFormat(); err != nil {
		c.Stop()
		return nil, err
	}

	return c, nil
}

// verifyFormat controlla che miniaudio ci abbia davvero consegnato il PCM che
// Vosk si aspetta. Se il driver avesse rifiutato la conversione la trascrizione
// sarebbe spazzatura: meglio fermarsi qui che segnalare silenziosamente
// "nessun testo riconosciuto".
func (c *Capture) verifyFormat() error {
	if got := c.device.SampleRate(); got != c.cfg.SampleRate {
		return fmt.Errorf("audio: il dispositivo è a %d Hz invece dei %d Hz richiesti", got, c.cfg.SampleRate)
	}
	if got := c.device.CaptureFormat(); got != malgo.FormatS16 {
		return fmt.Errorf("audio: il dispositivo restituisce il formato %v invece di s16", got)
	}
	if got := c.device.CaptureChannels(); got != 1 {
		return fmt.Errorf("audio: il dispositivo restituisce %d canali invece di mono", got)
	}
	return nil
}

// applyDeviceID seleziona un dispositivo di cattura esplicito, oppure quello
// predefinito di sistema quando cfg.DeviceID è negativo.
func (c *Capture) applyDeviceID(deviceCfg *malgo.DeviceConfig) error {
	c.name = "dispositivo predefinito"
	if c.cfg.DeviceID < 0 {
		return nil
	}

	devices, err := c.ctx.Devices(malgo.Capture)
	if err != nil {
		return fmt.Errorf("audio: elenco dispositivi fallito: %w", err)
	}
	if c.cfg.DeviceID >= len(devices) {
		return fmt.Errorf("audio: device_id %d non valido, trovati %d dispositivi di cattura", c.cfg.DeviceID, len(devices))
	}
	deviceCfg.Capture.DeviceID = devices[c.cfg.DeviceID].ID.Pointer()
	c.name = devices[c.cfg.DeviceID].Name()
	return nil
}

func (c *Capture) resolveName() string {
	devices, err := c.ctx.Devices(malgo.Capture)
	if err != nil {
		return "sconosciuto"
	}
	if c.cfg.DeviceID >= 0 && c.cfg.DeviceID < len(devices) {
		return devices[c.cfg.DeviceID].Name()
	}
	for _, d := range devices {
		if d.IsDefault != 0 {
			return d.Name() + " (predefinito)"
		}
	}
	return "predefinito"
}

// onData è la callback di miniaudio. Non deve mai bloccare.
func (c *Capture) onData(_, input []byte, _ uint32) {
	if len(input) == 0 {
		return
	}
	frame := make([]byte, len(input))
	copy(frame, input)

	select {
	case c.ch <- frame:
	default:
		// Il consumer è indietro: meglio scartare il frame che bloccare il
		// thread audio, per miniaudio un blocco qui significa un glitch.
	}
}

// Start avvia lo streaming. I blocchi diventano leggibili sul canale restituito
// da Frames.
func (c *Capture) Start() error {
	if err := c.device.Start(); err != nil {
		return fmt.Errorf("audio: avvio stream fallito: %w", err)
	}
	return nil
}

// Frames restituisce il canale dei blocchi PCM. Viene chiuso da Stop.
func (c *Capture) Frames() <-chan []byte { return c.ch }

// Stop ferma lo streaming e libera il dispositivo e il contesto.
func (c *Capture) Stop() {
	c.once.Do(func() {
		if c.device != nil {
			_ = c.device.Stop()
			c.device.Uninit()
		}
		if c.ctx != nil {
			c.ctx.Free()
		}
		close(c.ch)
	})
}

// Description restituisce un'etichetta leggibile del dispositivo in uso.
func (c *Capture) Description() string {
	return fmt.Sprintf("%s — %d Hz, s16 mono, periodo 100 ms", c.name, c.device.SampleRate())
}
