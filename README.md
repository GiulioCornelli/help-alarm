# help-alarm

`help-alarm` ascolta l'audio di una o più **telecamere IP**, trascrive ciò che
sente e segnala quando qualcuno pronuncia una **parola chiave**, per esempio
**"aiuto"**, indicando da quale telecamera proviene.

Tutto avviene **in locale**: l'audio non esce dalla rete, il riconoscimento
vocale (STT, speech-to-text) è fatto da un modello che gira sulla stessa
macchina e il programma non contatta nessun servizio esterno.

Quando una parola chiave viene riconosciuta, il programma stampa sul terminale

```
15:42:07 aiuto rilevato da ingresso (192.168.1.10), luce accesa
```

e scrive un record JSON nel log degli allarmi, con data e ora, parola chiave,
nome e indirizzo della telecamera.

> La «luce accesa» è per ora solo nel messaggio: l'accensione reale (GPIO o
> relè) non è ancora implementata. Vedi «Sviluppo futuro».

## Indice

- [Come funziona](#come-funziona)
- [Struttura del progetto](#struttura-del-progetto)
- [Tecnologie e librerie](#tecnologie-e-librerie)
- [A cosa serve ffmpeg](#a-cosa-serve-ffmpeg)
- [Il modello di riconoscimento vocale](#il-modello-di-riconoscimento-vocale)
- [Configurazione](#configurazione)
- [Avvio rapido con Docker](#avvio-rapido-con-docker)
- [Log e privacy](#log-e-privacy)
- [Rilevazione e cooldown](#rilevazione-e-cooldown)
- [Sviluppo e test](#sviluppo-e-test)
- [Sviluppo futuro](#sviluppo-futuro)

## Come funziona

Per ogni telecamera configurata il programma costruisce una catena di stadi
indipendente. Le catene girano in parallelo, ognuna nella propria goroutine:

```
  telecamera IP                                          allarme
 ┌─────────────┐  RTSP  ┌────────┐  PCM 16 kHz  ┌────────┐  testo  ┌─────────┐
 │ audio AAC / │ ─────▶ │ ffmpeg │ ───────────▶ │  Vosk  │ ──────▶ │ parola  │──▶ alert
 │ G.711 ...   │        │        │   mono s16   │  (STT) │         │ chiave? │
 └─────────────┘        └────────┘              └────────┘         └─────────┘
          × N telecamere (un ffmpeg e un recognizer per ciascuna, un solo modello)
```

1. **Ricezione.** Un processo ffmpeg per telecamera si collega allo stream RTSP,
   estrae la traccia audio e la converte nel formato richiesto dal
   riconoscitore. Il programma legge l'audio a blocchi da 100 ms.
2. **Trascrizione.** Ogni blocco viene passato a Vosk, che restituisce o
   un'*ipotesi parziale* (la persona sta ancora parlando) o il *testo
   definitivo* (Vosk ha riconosciuto la fine della frase).
3. **Ricerca della parola chiave.** Il testo viene normalizzato (minuscole, senza
   punteggiatura) e confrontato con le parole chiave di `config.json`, **a
   confine di parola**: `aiuto` scatta su «per favore aiuto» ma non su
   «aiutami».
4. **Allarme.** Se c'è una corrispondenza e non è un duplicato recente
   (cooldown), il programma stampa la riga sul terminale e scrive il record nel
   log degli allarmi.

### Scelte di robustezza

- **Un frame perso è meglio di un ritardo.** Fra ricezione e trascrizione il
  passaggio dei blocchi è non bloccante: se la trascrizione resta indietro, i
  blocchi in eccesso vengono scartati. Un audio che accumula ritardo
  ritarderebbe ogni allarme successivo.
- **Una telecamera che cade non ferma le altre.** Se lo stream si interrompe,
  ffmpeg termina e viene riavviato con attesa crescente (1 s, 2 s, 4 s... fino a
  30 s). L'attesa torna al minimo appena la telecamera manda di nuovo audio. Se
  una telecamera smette di inviare dati senza chiudere la connessione, ffmpeg se
  ne accorge dopo 10 s.
- **Modello condiviso.** Il modello Vosk è la parte pesante in memoria e viene
  caricato una sola volta. Ogni telecamera ha invece il proprio *recognizer*,
  perché tiene lo stato della frase in corso.
- **Arresto ordinato.** Con `Ctrl+C` o `SIGTERM` gli ffmpeg vengono fermati, la
  frase eventualmente rimasta a metà viene trascritta e analizzata, e solo dopo
  si liberano le risorse native.

## Struttura del progetto

```
help-alarm-IpCam/
├── main.go                  collega le parti: config → rtsp → stt → keyword → alert
├── config.json              configurazione (telecamere, parole chiave, log)
├── Dockerfile               immagine a tre stadi: deps, build, runtime (con ffmpeg)
├── docker-compose.yml       avvio con config e volume dei log
├── Makefile                 scorciatoie per Docker (make docker-up, docker-logs, ...)
│
├── cmd/checkvocab/          strumento: verifica se il modello conosce una parola
└── internal/
    ├── config/              lettura e validazione di config.json
    ├── rtsp/                ffmpeg per telecamera, riconnessione, credenziali nascoste
    ├── stt/                 wrapper di Vosk: modello condiviso, recognizer, parsing JSON
    ├── keyword/             normalizzazione e ricerca delle parole chiave
    ├── alert/               interfaccia Alerter, cooldown, allarme su terminale
    └── logging/             tre logger separati: console, file, allarmi
```

## Tecnologie e librerie

| Componente | Ruolo |
| --- | --- |
| **Go 1.23** | linguaggio del programma; goroutine e canali per le catene parallele |
| **[Vosk](https://alphacephei.com/vosk/)** | motore di riconoscimento vocale offline (basato su Kaldi) |
| **`github.com/alphacep/vosk-api/go`** | binding Go ufficiali di Vosk, versione 0.3.50 |
| **`libvosk.so`** | libreria nativa C++ di Vosk (0.3.45 nell'immagine), chiamata dai binding tramite **cgo** |
| **ffmpeg** | riceve lo stream RTSP e ne estrae l'audio convertito (vedi sotto) |
| **`log/slog`** (libreria standard) | logging strutturato, anche in JSON |
| **`os/exec`** (libreria standard) | avvio e controllo dei processi ffmpeg |
| **Docker** | confeziona tutto: compilatore, libreria, modello ed ffmpeg |
| **[MediaMTX](https://github.com/bluenviron/mediamtx)** | solo per i test: finto server RTSP |

L'unica dipendenza Go esterna è il binding di Vosk (`go.mod`). Tutto il resto
usa la libreria standard.

Vosk è scritto in C++ e viene chiamato tramite cgo: compilatore C, libreria
`libvosk.so` e modello sono già dentro l'immagine Docker, quindi non devi
installarli.

### Telecamere testate

| Produttore | Modello |
| --- | --- |
| TKH Security | SEI-T4325TI-H |

Altre telecamere che espongono l'audio via RTSP dovrebbero funzionare, perché
ffmpeg gestisce i codec più comuni, ma non sono state provate.

## A cosa serve ffmpeg

Le telecamere IP non mandano l'audio in un formato direttamente utilizzabile da
Vosk. Parlano **RTSP**, un protocollo di streaming, e comprimono l'audio con
codec diversi a seconda del modello (AAC, G.711, G.726, ...). Vosk invece
accetta soltanto **PCM a 16 bit, mono, a 16 000 Hz**.

ffmpeg fa da traduttore. Per ogni telecamera il programma lancia un comando
equivalente a:

```
ffmpeg -rtsp_transport tcp -timeout 10000000 -i rtsp://... \
       -map 0:a:0 -ac 1 -ar 16000 -f s16le pipe:1
```

| Opzione | Effetto |
| --- | --- |
| `-rtsp_transport tcp` | usa RTSP su TCP: niente pacchetti persi o riordinati, funziona anche dove l'UDP è filtrato |
| `-timeout 10000000` | dopo 10 s senza dati la connessione viene considerata caduta |
| `-i rtsp://...` | l'indirizzo della telecamera, preso da `config.json` |
| `-map 0:a:0` | prende solo la prima traccia audio; il **video non viene decodificato** |
| `-ac 1` | converte in mono |
| `-ar 16000` | ricampiona a 16 kHz |
| `-f s16le pipe:1` | scrive PCM grezzo a 16 bit su stdout, che il programma legge a blocchi da 100 ms |

Vantaggi di questa scelta: si supporta qualunque codec senza scrivere
decodificatori, e il programma Go resta semplice, perché legge solo byte già
pronti da stdout.

**ffmpeg vive solo nell'immagine Docker.** Per scelta di progetto non va
installato sul computer che esegue il programma. Se lanci l'eseguibile fuori da
Docker, esce subito con `ffmpeg non trovato: il programma va avviato con Docker`.

Un processo ffmpeg per telecamera significa che ogni stream è isolato: se uno si
blocca o cade, gli altri non se ne accorgono.

## Il modello di riconoscimento vocale

Il riconoscimento usa **`vosk-model-small-it-0.22`**, il modello italiano
«small» di Vosk (~48 MB). È scaricato durante la build dell'immagine, quindi
`docker compose up` funziona anche senza connessione.

Perché questo modello:

- gira su computer modesti e su Raspberry Pi (circa 300 MB di memoria);
- trascrive in tempo reale, anche con più telecamere in parallelo;
- è in italiano, la lingua dell'ambiente da sorvegliare.

Limiti da conoscere:

- Il modello è **solo italiano**: parole chiave in altre lingue possono non
  essere mai riconosciute. Lo strumento `cmd/checkvocab` verifica se il
  vocabolario del modello contiene una data parola.
- Essendo un modello piccolo, può sbagliare con audio lontano, rumoroso o di
  bassa qualità. Provalo sulle tue telecamere prima di fidarti.
- Il modello più grande `vosk-model-it-0.22` (~1,2 GB) è più accurato ma molto
  più pesante. Per usarlo basta scaricarlo e cambiare `model_path`.

Costo di CPU: circa **un core per telecamera**, quasi tutto dovuto a Vosk.
Dimensiona la macchina di conseguenza.

## Configurazione

Tutto si configura da `config.json`:

```json
{
  "cameras": [
    { "name": "ingresso", "url": "rtsp://utente:password@192.168.1.10:554/stream1" },
    { "name": "cortile",  "url": "rtsp://utente:password@192.168.1.11:554/stream1" }
  ],
  "keywords": ["aiuto", "soccorso", "emergenza"],
  "model_path": "models/vosk-model-small-it-0.22",
  "sample_rate": 16000,
  "log_file": "logs/help-alarm.log",
  "alert_log_file": "logs/help-alarm-alerts.log",
  "log_level": "info",
  "log_transcriptions": true,
  "cooldown_seconds": 3
}
```

| Campo | Significato |
| --- | --- |
| `cameras` | telecamere da ascoltare, ognuna con `name` e `url`. **Obbligatorio**, almeno una |
| `keywords` | parole o frasi da cercare nella trascrizione. Obbligatorio |
| `model_path` | cartella del modello Vosk |
| `sample_rate` | deve essere `16000`, il valore richiesto da Vosk |
| `log_file` | messaggi di funzionamento (solo con `-v`) |
| `alert_log_file` | file che riceve **solo** gli allarmi. Se vuoto, finiscono in `log_file` |
| `log_level` | `debug`, `info`, `warn` o `error` |
| `log_transcriptions` | se `true`, con `-v` registra ogni frase riconosciuta |
| `cooldown_seconds` | secondi minimi fra due allarmi della stessa parola sulla stessa telecamera |

Le chiavi omesse prendono i valori mostrati sopra, tranne `cameras` e
`keywords`. I percorsi relativi sono risolti rispetto alla cartella di
`config.json`. Una configurazione errata fa fallire l'avvio con un messaggio
chiaro.

### Telecamere

| Campo | Significato |
| --- | --- |
| `name` | nome mostrato negli allarmi e nei log; deve essere **unico** |
| `url` | indirizzo RTSP, con utente e password se richiesti; schema `rtsp://` o `rtsps://` |

Il percorso dello stream dipende dal produttore (per esempio `/stream1`,
`/Streaming/Channels/101` o `/cam/realmonitor?channel=1&subtype=0`): si trova
nel manuale o nell'interfaccia web della telecamera. Se esiste, conviene usare
il sotto-stream a bassa risoluzione: l'audio è lo stesso e la telecamera lavora
meno.

**Aggiungere una telecamera** significa aggiungere una voce a `cameras` e
riavviare. Ogni telecamera avvia un proprio ffmpeg e un proprio recognizer.

**Credenziali.** Utente e password non compaiono mai nei log, sul terminale o
nei messaggi di errore: il programma mostra solo `name` e indirizzo, e ripulisce
anche l'output di ffmpeg, che ripete l'URL completo. La password resta però in
chiaro dentro `config.json`: proteggi il file di conseguenza e non metterlo in
un repository pubblico.

**Telecamera senza audio.** Se la traccia audio manca o è disattivata, ffmpeg
fallisce e il programma riprova a intervalli crescenti. L'errore si vede solo in
modalità dettagliata (`-v`).

### Parole chiave

Le parole chiave sono normalizzate come il testo trascritto: minuscole, senza
punteggiatura, duplicati eliminati. `Aiuto`, `aiuto` e `AIUTO!` sono la stessa
chiave. Sono ammesse anche frasi di più parole, che scattano solo se le parole
sono consecutive:

```json
{ "keywords": ["aiuto", "per favore aiuto", "sono in pericolo"] }
```

## Avvio rapido con Docker

L'unico requisito sul computer è **Docker** (con Compose). Non servono Go, gcc,
Vosk, il modello né ffmpeg: sono tutti nell'immagine.

> **Prima di avviare, configura bene `config.json`** (sezione precedente). Il
> programma legge il file all'avvio e **si ferma subito** se manca anche una sola
> telecamera valida o una parola chiave. Se gli URL sono sbagliati, invece,
> parte ma non sente nulla: ffmpeg non riesce a collegarsi e il programma
> continua a riprovare in silenzio.

### 1. Controlla la configurazione

Prima di lanciare il comando verifica che:

- `cameras` contenga almeno una telecamera, con `name` unico e `url` completo
  (utente, password, indirizzo, porta e percorso dello stream, non solo l'IP);
- ogni telecamera abbia l'**audio attivo** nelle sue impostazioni (spesso è
  disattivato di default) ed esponga lo stream **RTSP**;
- la macchina con Docker **raggiunga le telecamere** in rete;
- `keywords` contenga le parole da cercare, in italiano;
- il formato JSON sia corretto (virgole, virgolette, parentesi).

Puoi provare l'URL prima con VLC (*Apri flusso di rete*): se VLC lo apre e senti
l'audio, l'URL è giusto.

### 2. Avvia

```bash
docker compose up -d --build   # costruisce l'immagine e avvia in background
docker compose logs -f         # segue l'output
```

La prima build scarica libreria e modello e compila il programma, quindi ci
mette qualche minuto. Le successive riusano la cache.

### 3. Verifica che funzioni

Il container parte in **modalità silenziosa**: scrive solo gli allarmi, quindi
l'assenza di output è normale. Per controllare che le telecamere siano
collegate e che le frasi vengano capite, avvia una prova in modalità
dettagliata (ferma prima il container normale: `docker compose stop`):

```bash
docker compose run --rm help-alarm -v
```

Con `-v` devi vedere `telecamera connessa` per ogni telecamera e le frasi che
pronunci vicino al microfono della telecamera. Se compare `stream interrotto,
nuovo tentativo`, controlla URL, credenziali, audio attivo e rete. Quando va
tutto bene, riavvia in modo normale con `docker compose up -d`.

### 4. Comandi quotidiani

```bash
docker compose up -d        # avvia
docker compose logs -f      # segue l'output
docker compose stop         # ferma in modo ordinato
docker compose down         # ferma e rimuove il container
```

Dopo aver modificato `config.json` (che è montato dall'host, non copiato
nell'immagine) basta riavviare, senza ricostruire:

```bash
docker compose restart
```

Per far ripartire il programma da solo dopo un riavvio della macchina,
decommenta `restart: unless-stopped` in `docker-compose.yml`.

### Cosa configura il compose

| Direttiva | Motivo |
| --- | --- |
| bind mount `config.json` (sola lettura) | modifichi la configurazione dall'host senza ricostruire |
| volume `logs` | i log restano in un volume Docker e sopravvivono alle ricostruzioni |
| `user: "0:0"` | un volume vuoto nasce di `root`, e serve potervi scrivere i log |
| `stop_signal: SIGTERM`, `stop_grace_period: 10s` | il programma ferma gli stream e chiude i log in ordine |

Il container usa la rete bridge predefinita di Docker, che raggiunge le
telecamere sulla LAN. Se non le raggiunge, decommenta `network_mode: host` nel
compose.

### Come è fatta l'immagine

Il `Dockerfile` ha tre stadi, così l'immagine finale non contiene gli strumenti
di compilazione:

1. **deps**: scarica `libvosk.so`, `vosk_api.h` e il modello italiano.
2. **build**: compila il binario con cgo, in un'immagine con Go e gcc.
3. **runtime**: contiene solo binario, libreria, modello ed **ffmpeg**.

L'architettura è rilevata automaticamente (`TARGETARCH` di BuildKit): lo stesso
Dockerfile compila sia su PC a 64 bit sia su Raspberry Pi (arm64).

### Dove sono i log

I log non vanno nella cartella `logs/` del progetto ma nel volume Docker
`help-alarm-stt_logs`:

```bash
docker compose exec help-alarm tail -f /app/logs/help-alarm-alerts.log
docker cp help-alarm:/app/logs/help-alarm-alerts.log .    # copia un file sull'host
docker compose down -v                                    # cancella anche i log
```

Attenzione: `down -v` è irreversibile. Copia prima fuori ciò che ti serve.

## Log e privacy

Senza `-v` il programma è **muto**: nessuna parola pronunciata raggiunge il
terminale o il disco, tranne l'allarme stesso.

| Destinazione | Senza `-v` | Con `-v` |
| --- | --- | --- |
| **console** | solo la riga di allarme | avvio, telecamere, trascrizioni, errori, arresto |
| **`help-alarm.log`** | non viene creato | messaggi di funzionamento |
| **`help-alarm-alerts.log`** | sempre scritto | sempre scritto |

Il silenzio è imposto dentro il logger, non nei punti in cui si scrive: così non
basta dimenticare di togliere una stampa perché una frase finisca a schermo o su
disco.

Il log degli allarmi è in JSON, un record per riga, pensato per essere letto da
un altro programma (per esempio con `tail -f`):

```json
{"time":"2026-09-29T16:10:20.004Z","level":"INFO","msg":"aiuto rilevato da ingresso (192.168.1.10), luce accesa","timestamp":"2026-09-29T16:10:20.003Z","keyword":"aiuto","camera":"ingresso","camera_ip":"192.168.1.10"}
```

Ogni record contiene `timestamp`, `keyword`, `camera` e `camera_ip` (senza
credenziali). **Non contiene la frase** in cui la parola è stata trovata: le
altre parole pronunciate non vengono mai registrate, per scelta.

Esempio di log in modalità dettagliata:

```
INFO avvio help-alarm keywords="[aiuto soccorso]" telecamere="[ingresso (192.168.1.10) cortile (192.168.1.11)]" cooldown=3s
INFO telecamera connessa camera=ingresso camera_ip=192.168.1.10
WARN stream interrotto, nuovo tentativo camera=cortile camera_ip=192.168.1.11 errore="... Connection refused" attesa=1s
INFO trascrizione camera=ingresso camera_ip=192.168.1.10 testo="per favore aiuto"
```

Opzioni da riga di comando:

```
-config percorso   file di configurazione (default: config.json)
-v, -verbose       modalità dettagliata: mostra e salva anche le trascrizioni
```

## Rilevazione e cooldown

Le parole chiave vengono cercate sia nelle **ipotesi parziali** sia nelle frasi
definitive. Così il sistema reagisce mentre la persona sta ancora parlando,
senza aspettare la fine della frase: in un'emergenza ogni secondo conta.

Vosk emette una trascrizione ogni 100 ms, e finché la persona parla la parola
resta nell'ipotesi parziale. Senza protezioni un solo «aiuto» produrrebbe decine
di allarmi identici in un secondo. Il **cooldown** lo evita: per ogni coppia
(telecamera, parola chiave) i match successivi entro la finestra configurata
vengono scartati.

- Una parola chiave diversa non è influenzata.
- La stessa parola sentita da un'altra telecamera produce il proprio allarme.
- Dopo la finestra, lo stesso match scatta di nuovo.

Valori di `cooldown_seconds`: `3` (default), `0` per disattivare il filtro, un
valore negativo è rifiutato. Per una sorveglianza continua 2-5 secondi sono un
buon compromesso: troppo alto rischia di perdere un allarme reale, troppo basso
riproduce la raffica.

## Sviluppo e test

Tutto passa da Docker: non serve installare Go, un compilatore C o librerie
sull'host.

### Eseguire i test

I test girano dentro lo stadio `build` del Dockerfile, che ha già Go, gcc e la
libreria Vosk. Non richiedono né ffmpeg né telecamere.

```bash
docker build --target build -t help-alarm:test .
docker run --rm -e LD_LIBRARY_PATH=/src/third_party/vosk help-alarm:test go test ./...
```

`LD_LIBRARY_PATH` serve perché nello stadio `build` la libreria non si trova nel
percorso definitivo dell'immagine finale. Dopo una modifica al codice basta
ripetere i due comandi.

Per analisi statica e formattazione:

```bash
docker run --rm -e LD_LIBRARY_PATH=/src/third_party/vosk help-alarm:test go vet ./...
docker run --rm help-alarm:test gofmt -l .
```

### Cosa coprono i test

Normalizzazione e ricerca a confine di parola, caricamento e validazione della
configurazione (anche l'assenza di password nei messaggi di errore), cooldown
per telecamera, formato dei log, parsing del JSON di Vosk e ricezione degli
stream: blocchi da 100 ms, riconnessione, chiusura ordinata, assenza di password
nei log. Nei test degli stream ffmpeg è sostituito da un finto processo che
emette PCM.

### Prova end-to-end senza telecamere

Il riconoscimento reale si prova con un server RTSP di prova e un file audio che
contiene la parola chiave:

```bash
docker network create ha-test
docker run -d --name mtx --network ha-test bluenviron/mediamtx
docker run -d --network ha-test -v "$PWD":/in:ro --entrypoint ffmpeg help-alarm \
  -re -stream_loop -1 -i /in/aiuto.wav -c:a aac -f rtsp rtsp://mtx:8554/cam1
```

Poi metti `rtsp://mtx:8554/cam1` come telecamera in `config.json` e avvia il
programma sulla stessa rete (`--network ha-test`).

## Sviluppo futuro

L'accensione della luce non è ancora implementata, ma la base è pronta:
`internal/alert` espone l'interfaccia

```go
type Alerter interface {
    Name() string
    Trigger(Event) error
}
```

oggi implementata solo da `TerminalAlerter`. Per accendere una luce basterà
aggiungere una seconda implementazione (con `go-rpio` o `periph.io`, per un
relè o un GPIO) e passarla al `listener` in `main.go`, senza toccare gli altri
package. Il container dovrà poi avere accesso al dispositivo GPIO tramite
`devices:` nel compose.
