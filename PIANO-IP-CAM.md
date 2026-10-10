# Piano: audio da telecamere IP (RTSP) al posto del microfono locale

## Context
Oggi `help-alarm` cattura l'audio da un microfono locale (`internal/audio`, malgo) e lo passa a un unico recognizer Vosk. Serve invece ascoltare l'audio di una serie di telecamere IP (inizialmente 4, in futuro di più) via RTSP, e indicare nel log da quale telecamera proviene l'allarme. Il microfono locale viene rimosso.

Scelte confermate: RTSP, 4 telecamere (scalabile), solo telecamere IP (microfono locale e malgo rimossi), ffmpeg solo nell'immagine Docker, identità telecamera (nome e/o IP) nel log degli allarmi.

## Approccio
Una pipeline per telecamera, tutte indipendenti, con modello Vosk condiviso:

```
per ogni telecamera:  ffmpeg (RTSP → PCM s16le 16 kHz mono su stdout) → Recognizer → keyword.Matcher → alert
```

- **Decodifica con ffmpeg come sottoprocesso** (`exec.CommandContext`): evita cgo/librerie RTSP in Go, gestisce qualsiasi codec audio (AAC, G.711, ...), resampling a 16 kHz mono incluso. Comando tipo: `ffmpeg -rtsp_transport tcp -i <url> -vn -ac 1 -ar 16000 -f s16le -loglevel error pipe:1`. `-vn` scarta il video (ffmpeg non decodifica video non mappato).
- **Riconnessione automatica** con backoff esponenziale (es. 1s → 30s max): una telecamera offline non deve fermare le altre. Il reset del backoff avviene quando arriva audio.
- **Scalabilità**: ogni telecamera = 1 goroutine di lettura + 1 `Recognizer` (Vosk non è thread-safe per recognizer, ma il `Model` è condivisibile). Aggiungere telecamere = aggiungere una voce in `config.json`.

## Modifiche per file

- `internal/audio/capture.go` → **sostituire** con `internal/source` (o `internal/rtsp`): tipo `Stream{Name, URL}` con `Frames(ctx) <-chan []byte` che avvia ffmpeg, legge blocchi da 100 ms (3200 byte) e li pubblica; gestisce riavvio/backoff. Mantenere la stessa semantica del vecchio capture: invio **non bloccante** (scarta il frame se il consumatore è in ritardo). Rimuovere `-devices` da `main.go`, `device_id`, dipendenza `malgo` da `go.mod`.
- `internal/stt/recognizer.go`: oggi `Open(modelPath)` carica anche il modello. Separare `LoadModel(path)` (una volta) da `NewRecognizer(model)` (per telecamera); `Close` libera solo il recognizer, il modello viene liberato alla fine. Riutilizzare `checkModel`, `parseText`, `Accept`, `Flush` invariati.
- `internal/config/config.go`: sostituire `device_id` con `cameras: [{ "name": "ingresso", "url": "rtsp://user:pass@192.168.1.10:554/stream1" }]`. Validazione: almeno una telecamera, nomi non vuoti e **unici**, URL con schema `rtsp://`/`rtsps://`. Aggiungere un metodo che restituisce l'host (IP) dell'URL **senza credenziali**.
- `internal/alert/alert.go`: aggiungere `Camera` (nome) e `Host` (IP) a `Event`; `Cooldown.Allow` con chiave `camera + "\x00" + keyword` (cooldown per telecamera, altrimenti un "aiuto" su una camera silenzierebbe le altre). `Message` diventa es. `"%s rilevato (camera %s), luce accesa"`; il log JSON degli allarmi aggiunge i campi `camera` e `camera_ip`.
- `main.go`: `run()` carica il modello una volta, poi per ogni telecamera avvia una goroutine (`sync.WaitGroup`) con recognizer, stream e `trigger` (il `Matcher` è condivisibile se resta immutabile; verificare). Shutdown ordinato su SIGINT/SIGTERM: annulla il contesto, ferma ffmpeg, `Flush()` per ogni recognizer, attende il WaitGroup. `trigger` riceve nome/host della telecamera.
- `config.json`, `README.md`, `CLAUDE.md`: aggiornare (sezioni architettura, configurazione, log, Docker).
- `Dockerfile` / `docker-compose.yml`: installare `ffmpeg` nell'immagine runtime; rimuovere `devices: /dev/snd` e `group_add`. Il container deve raggiungere la rete delle telecamere (rete bridge di default va bene; `network_mode: host` solo se serve).
- Setup locale (`scripts/setup.sh`, README): ffmpeg diventa requisito; nessun più requisito ALSA/malgo (si può togliere anche il bisogno di gcc per malgo, ma Vosk usa ancora cgo).

## Vincoli da preservare
- **Privacy**: senza `-v` nessuna parola pronunciata esce dal programma tranne l'allarme; `Event` continua a non contenere la frase. Nome/IP telecamera sono ammessi nel log degli allarmi.
- **Credenziali RTSP mai nei log**: mascherare `user:pass@` in log, messaggi di errore (compreso l'output di ffmpeg su stderr, che può echeggiare l'URL) e nella riga di avvio. Mostrare solo `name` e `host`.
- Il logging di funzionamento (verbose) deve includere il nome telecamera in ogni record (`log.With("camera", name)`).
- I test devono restare eseguibili senza ffmpeg né telecamere.

## Verifica
1. Test unitari (`make test`): config (telecamere mancanti/duplicate/URL non valido, host senza credenziali), cooldown per chiave telecamera+parola, `Event`/messaggio con camera, parsing/mascheramento URL. Per lo stream, test con un finto eseguibile (es. `ffmpeg` sostituito da un comando che emette PCM) o con interfaccia iniettabile per il lettore.
2. Prova end-to-end senza telecamere reali: server RTSP di test (es. MediaMTX + `ffmpeg -re -stream_loop -1 -i file_con_aiuto.wav -f rtsp rtsp://localhost:8554/test`), configurare 2 telecamere, verificare che `logs/help-alarm-alerts.log` contenga `camera` e `camera_ip` corretti e che il cooldown sia per telecamera.
3. Resilienza: killare il server RTSP e riavviarlo → il programma si riconnette senza riavvio e le altre camere continuano.
4. Con le 4 telecamere reali: controllare l'uso CPU (Vosk small ≈ 1 core per stream), poi `docker compose up -d` con ffmpeg nell'immagine.

## Domande aperte (non bloccanti, default scelti)
- Telecamere con audio non-RTSP o senza traccia audio: ffmpeg fallirà, il programma lo registra e riprova con backoff (default).
- Credenziali nell'URL in `config.json` (file già montato `:ro`); in futuro eventualmente da variabili d'ambiente.
