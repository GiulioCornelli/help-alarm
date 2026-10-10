# help-alarm

Sistema in Go che ascolta l'audio di una serie di **telecamere IP** (via RTSP),
trascrive in tempo reale ciò che sentono usando il riconoscimento automatico del
parlato (STT) **interamente in locale** e segnala la presenza di una parola
chiave, per esempio **"aiuto"**, indicando quale telecamera l'ha sentita.

Quando la parola viene riconosciuta il programma stampa sul terminale:

```
15:42:07 aiuto rilevato da ingresso (192.168.1.10), luce accesa
```

e scrive lo stesso messaggio in un file di log JSON con data, ora, nome e
indirizzo della telecamera.

Nessun dato audio lascia la rete locale: il programma riceve l'audio dalle
telecamere, ma modello linguistico e libreria STT sono locali e non c'è alcuna
chiamata verso servizi esterni.

## Come funziona

Per ogni telecamera il programma costruisce una catena di stadi, ognuno in un
package separato. Le catene sono indipendenti e girano in parallelo:

```
  telecamera              testo                          allarme
 ┌──────────┐  PCM s16   ┌──────────┐   frasi   ┌──────────┐
 │   rtsp   │──────────▶ │   stt    │──────────▶ │ keyword  │──▶ alert
 │ (ffmpeg) │   16 kHz   │  (Vosk)  │           └──────────┘
 └──────────┘   mono     └──────────┘
     × N telecamere, un modello Vosk condiviso
```

1. **`internal/rtsp`** avvia un processo **ffmpeg per telecamera**: ffmpeg si
   collega allo stream RTSP, estrae la traccia audio qualunque sia il codec
   (AAC, G.711, ...) e la converte in PCM s16le mono a 16 kHz, il formato che
   Vosk richiede. Il programma legge i byte già pronti a blocchi da 100 ms.

2. **`internal/stt`** usa [Vosk](https://alphacephei.com/vosk/) tramite i binding Go
   ufficiali (`github.com/alphacep/vosk-api/go`). Ogni 100 ms di audio viene
   passato a `AcceptWaveform`, che restituisce una trascrizione: una **ipotesi
   parziale** mentre la persona sta ancora parlando, il **testo definitivo** quando
   Vosk riconosce la fine della frase. Il JSON di risposta viene decodificato e
   ridotto al solo campo testuale.

3. **`internal/keyword`** confronta il testo con le parole chiave di
   `config.json` e **`internal/alert`** reagisce a ogni occorrenza trovata.

La ricezione e la trascrizione di ogni telecamera girano in goroutine separate
collegate da un canale. Il modello Vosk, la parte pesante in memoria, viene
caricato una volta sola; ogni telecamera ha però il proprio recognizer, perché
tiene lo stato della frase in corso.

### L'audio non deve restare indietro

Il canale fra ricezione e trascrizione usa un invio **non bloccante**: se la
trascrizione è in ritardo, il blocco viene scartato. Un frame perso (100 ms) è
preferibile a un audio che accumula ritardo rispetto alla realtà, che in un
sistema di allarme ritarderebbe ogni segnalazione successiva.

### Telecamere che cadono

Se una telecamera non risponde o lo stream si interrompe, ffmpeg termina e
viene riavviato da solo dopo un'attesa crescente (1 s, 2 s, 4 s... fino a
30 s). L'attesa torna al minimo appena la telecamera manda di nuovo audio.
Una telecamera offline non ferma mai le altre. Se una telecamera smette di
mandare dati senza chiudere la connessione, ffmpeg se ne accorge dopo 10 s.

## Struttura delle cartelle

```
help-alarm-STT/
├── main.go                  cablaggio: config → rtsp → stt → keyword → alert
├── config.json              configurazione utente
├── Makefile                 build, test e run con i flag cgo già impostati
├── Dockerfile               immagine multi-stadio: deps, build, runtime
├── docker-compose.yml       avvio con volumi già configurati
├── .dockerignore            esclude dall'immagine ciò che si ricrea
├── scripts/setup.sh         scarica libreria nativa Vosk + modello italiano
├── TASKS.md                 elenco delle attività del progeto
│
├── internal/
│   ├── config/              lettura e validazione di config.json
│   ├── rtsp/                ffmpeg per telecamera, riconnessione, credenziali nascoste
│   ├── stt/                 wrapper attorno a Vosk, parsing del JSON
│   ├── keyword/             normalizzazione del testo e ricerca parole chiave
│   ├── alert/               interfaccia Alerter, cooldown e implementazione su terminale
│   └── logging/             log/slog su file JSON e su console testuale
│
├── models/                  modello Vosk (scaricato, non nel repository)
├── third_party/vosk/        libvosk.so e vosk_api.h (scaricati, non nel repository)
└── logs/                    log di esecuzione
```

## Requisiti

Il programma **si esegue con Docker**, e l'unico requisito è Docker stesso:
l'immagine contiene Go, il compilatore C, la libreria Vosk, il modello
linguistico e **ffmpeg**. ffmpeg non va installato sull'host: vive solo
nell'immagine.

Servono inoltre:

- telecamere IP che espongano l'audio via RTSP (l'audio deve essere attivo
  nelle impostazioni della telecamera);
- che la macchina con Docker raggiunga le telecamere in rete.

Per compilare e lanciare i test in locale, senza Docker, servono:

- Go 1.21 o superiore (sviluppato con Go 1.23)
- un compilatore C (`gcc` o `clang`): Vosk usa cgo

I test non richiedono né ffmpeg né telecamere.

## Avvio con Docker

Il modo previsto: non devi installare Go, gcc, il modello Vosk né ffmpeg.
Prima inserisci le telecamere in `config.json` (vedi «Telecamere»).

```bash
docker compose up -d        # costruisce l'immagine e avvia
docker compose logs -f      # segue l'output
docker compose stop         # ferma in modo ordinato
docker compose down         # ferma e rimuove il container
```

L'operatività quotidiana si riduce a `docker compose up -d`, e grazie a
`restart: unless-stopped` il programma ripparte da solo dopo un riavvio della
macchina.

Così com'è, il container parte in modalità silenziosa: scrive solo gli allarmi.
Per la modalità dettagliata con le trascrizioni, senza toccare il compose:

```bash
docker compose run --rm help-alarm -v
```

### Cosa mette dentro il compose

| Direttiva | Motivo |
| --- | --- |
| `user: "0:0"` | un volume vuoto nasce di `root`, e serve potervi scrivere i log |
| bind mount `config.json` | modifichi la configurazione dall'host senza ricostruire |
| volume `logs` | i log finiscono in un volume di Docker, non nel progetto |
| `stop_signal: SIGTERM` | arrivano al programma, che ferma gli stream e chiude i log in ordine |

Il container usa la rete bridge predefinita di Docker, che raggiunge le
telecamere sulla LAN. Se non le raggiunge (per esempio con telecamere in
multicast), decommenta `network_mode: host` nel compose.

### Dove finiscono i log

I log **non** scrivono nella cartella `logs/` del progetto, ma in un volume
Docker chiamato `help-alarm-stt_logs`. Il vantaggio è che `docker compose down`
non tocca i dati e il volume sopravvive a ricostruzioni dell'immagine.

Per leggerli:

```bash
docker compose exec help-alarm tail -f /app/logs/help-alarm.log
docker compose exec help-alarm tail -f /app/logs/help-alarm-alerts.log
```

Oppure copiarli fuori quando serve un file:

```bash
docker cp help-alarm:/app/logs/help-alarm-alerts.log .
```

Per cancellarli del tutto, compresi i dati storici:

```bash
docker compose down -v        # rimuove anche il volume dei log
```

Attenzione: `-v` è irreversibile. Se ti serve svuotare i log ma tenerli salvi,
prima copiali fuori con `docker cp`.

### L'architettura dentro l'immagine

Il `Dockerfile` è a tre stadi, così l'immagine finale contiene solo il
programma e non gli strumenti di compilazione:

1. **deps** — scarica `libvosk.so`, `vosk_api.h` e il modello italiano
2. **build** — compila il binario con cgo, in un'immagine con Go e gcc
3. **runtime** — copia solo binario, libreria e modello

L'architettura viene rilevata automaticamente (`TARGETARCH` di BuildKit), quindi
lo stesso Dockerfile compila sia su PC a 64 bit sia su Raspberry Pi.

Il modello Vosk pesa 48 MB e finisce nell'immagine: è un costo fisso di una
build, ma in cambio `docker compose up` funziona anche senza connessione.

## Compilazione e test in locale

Per eseguire il programma basta Docker: `docker compose up -d`. Fuori dal
container ffmpeg manca, e il programma si ferma subito con l'errore
`ffmpeg non trovato: il programma va avviato con Docker`.

Per compilare e lanciare i test in locale va eseguito prima `./scripts/setup.sh`.

Lo script scarica due cose dentro il progetto, senza usare `sudo` e senza
installare nulla a sistema:

- **`third_party/vosk/libvosk.so`** e **`vosk_api.h`** — la libreria nativa C di
  Vosk, necessaria perché i binding Go la chiamano tramite cgo;
- **`models/vosk-model-small-it-0.22/`** (~48 MB) — il modello linguistico
  italiano, l'unico supportato per ora dal progetto.

Se lo script non è adatto alla tua architettura, scarica `libvosk.so` da
<https://github.com/alphacep/vosk-api/releases> e mettilo in `third_party/vosk/`,
oppure usa Docker, che risolve il caso da solo.

## Build e test

```bash
make setup      # scarica libreria Vosk e modello (una tantum)
make build      # compila in ./help-alarm
make test       # esegue i test (senza ffmpeg né telecamere)
make docker-up  # costruisce l'immagine, avvia e segue i log
```

Il `Makefile` è necessario perché i binding Go di Vosk hanno bisogno dei flag
`CGO_CFLAGS` e `CGO_LDFLAGS` per trovare la libreria nativa. Se preferisci usare
`go` direttamente, esportali a mano:

```bash
export CGO_CFLAGS="-I$PWD/third_party/vosk"
export CGO_LDFLAGS="-L$PWD/third_party/vosk -lvosk -Wl,-rpath,$PWD/third_party/vosk"
go build -o help-alarm .
```

Se `go` non è nel `PATH`, come nel caso di un'installazione manuale in
`/usr/local/go/bin`, anteggi `export PATH=$PATH:/usr/local/go/bin`.

### Opzioni da riga di comando

```
-config percorso   file di configurazione da usare (default: config.json)
-v, -verbose       modalità dettagliata: mostra e salva tutto (trascrizioni comprese)
```

Senza `-v` il programma tace. Sul terminale compare **solo** la riga di
allarme, e nessuna parola pronunciata viene scritta o mostrata, a meno di non
essere una parola chiave configurata.

## Configurazione

Tutto si configura da `config.json`:

```json
{
  "cameras": [
    { "name": "ingresso", "url": "rtsp://utente:password@192.168.1.10:554/stream1" },
    { "name": "cortile",  "url": "rtsp://utente:password@192.168.1.11:554/stream1" }
  ],
  "keywords": [
    "aiuto",
    "soccorso",
    "emergenza"
  ],
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
| `keywords` | parole o frasi da cercare nella trascrizione |
| `model_path` | cartella del modello Vosk |
| `cameras` | telecamere da ascoltare, ognuna con `name` e `url` RTSP (obbligatorio) |
| `sample_rate` | deve essere `16000`, è il valore che Vosk richiede |
| `log_file` | file con i messaggi normali (avvio, trascrizioni, errori) |
| `alert_log_file` | file che riceve **solo** gli allarmi |
| `log_level` | `debug`, `info`, `warn` o `error` |
| `log_transcriptions` | se `true`, logga ogni frase riconosciuta |
| `cooldown_seconds` | secondi minimi fra due allarmi della stessa parola chiave |

Le chiavi omesse prendono il valore di default mostrato sopra, tranne
`cameras`, che è obbligatoria. I percorsi
relativi sono risolti rispetto alla cartella che contiene il file di
configurazione, quindi si può lanciare il programma da qualsiasi directory.
Un file di configurazione errato fa fallire l'avvio con un messaggio esplicito,
non in silenzio.

### Parole chiave

Le parole chiave sono normalizzate allo stesso modo del testo trascritto:
minuscole, senza punteggiatura. `Aiuto`, `aiuto` e `AIUTO!` sono la stessa
chiave, e i duplicati vengono eliminati.

Il confronto avviene **a confine di parola**, non con una ricerca di sottostringa:
`aiuto` scatta su "per favore aiuto" ma non su "aiutami" né su "inaiuto".

Sono supportate anche le frasi di più parole, che scattano solo se le parole
compaiono consecutive:

```json
{ "keywords": ["aiuto", "per favore aiuto", "sono in pericolo"] }
```

### Telecamere

Ogni voce di `cameras` è una telecamera:

| Campo | Significato |
| --- | --- |
| `name` | nome della telecamera, compare negli allarmi e nei log; deve essere unico |
| `url` | indirizzo RTSP dello stream, con utente e password se richiesti |

L'URL esatto dipende dal produttore (per esempio `/stream1`, `/Streaming/Channels/101`
oppure `/cam/realmonitor?channel=1&subtype=0`): si trova nel manuale della
telecamera o nella sua interfaccia web. Conviene usare il sotto-stream a bassa
risoluzione, se esiste: l'audio è lo stesso e la telecamera lavora meno.

Per aggiungere una telecamera basta aggiungere una voce e riavviare il
programma. Ogni telecamera avvia un proprio processo ffmpeg e un proprio
riconoscitore Vosk: il costo è di circa un core di CPU per telecamera, quasi
tutto dovuto a Vosk.

**Credenziali.** Password e utente non compaiono mai nei log né sul terminale:
il programma mostra solo `name` e l'indirizzo della telecamera, e ripulisce
anche i messaggi di errore di ffmpeg, che ripetono l'URL completo. La password
resta però in chiaro dentro `config.json`, che il compose monta in sola
lettura: proteggi il file di conseguenza.

Se una telecamera non ha la traccia audio, o ha l'audio disattivato, ffmpeg
fallisce e il programma riprova a intervalli crescenti; in modalità
dettagliata (`-v`) l'errore compare nel log.

## Log

Il silenzio è il comportamento predefinito, e non una semplice questione di
livello di log: senza `-v` nessuna parola pronunciata lascia il programma.

| Cosa | Senza `-v` | Con `-v` |
| --- | --- | --- |
| **console** | solo la riga di allarme | avvio, telecamere, trascrizioni, errori, arresti |
| **`logs/help-alarm.log`** | non viene neppure creato | messaggi di funzionamento |
| **`logs/help-alarm-alerts.log`** | **sempre** scritto | **sempre** scritto |

Il silenzio è applicato nel logger, non nei chiamanti: `Console` e `File`
scrivono su un Writer che scarta tutto. Così non basta dimenticarsi di togliere
una `stampa` perché una parola finisca a schermo o su disco.

Le tre destinazioni hanno logger separati perché non si mescolino:

| Destinazione | Contenuto |
| --- | --- |
| **`logs/help-alarm.log`** | messaggi di funzionamento, solo in modalità dettagliata |
| **`logs/help-alarm-alerts.log`** | **solo** gli allarmi, in JSON con timestamp |
| **console** | la sola riga di allarme, oppure tutto in dettagliato |

I due file sono in formato JSON, un record per riga, pensati per essere letti da
un programma esterno.

```json
// logs/help-alarm-alerts.log
{"time":"2026-09-29T16:10:20.004Z","level":"INFO","msg":"aiuto rilevato da ingresso (192.168.1.10), luce accesa","timestamp":"2026-09-29T16:10:20.003Z","keyword":"aiuto","camera":"ingresso","camera_ip":"192.168.1.10"}
```

Ogni record di allarme riporta `timestamp` (l'istante esatto in cui è stato
rilevato), `keyword` (la parola configurata che ha scattato), `camera` (il nome
della telecamera) e `camera_ip` (il suo indirizzo, senza credenziali). **Non contiene la
frase in cui la parola è stata trovata**: le altre parole pronunciate non vengono
registrate, per scelta.

Tenere gli allarmi in un file a parte permette a un altro programma — o a un
invio di rete — di seguirlo in tempo reale con `tail -f`, senza doverlo filtrare
dal rumore delle trascrizioni.

Per non ripetere la riga sul terminale, l'allarme scrive il messaggio sul
terminale con una `fmt.Fprintf` e sul file col logger: le due destinazioni hanno
logger separati proprio per questo.

La riga riporta la parola udita e non una parola fissa: un allarme su
`emergenza` scrive `emergenza rilevato da ingresso (192.168.1.10), luce accesa`, perché `Event` non ha
nemmeno un campo dove mettere il resto della frase.

Se `alert_log_file` viene lasciato vuoto, gli allarmi finiscono nel file dei
messaggi normali.

Con Docker i log finiscono in un volume dedicato, quindi `docker compose down`
non porta via la traccia. Vedi la sezione «Dove finiscono i log».

## Comportamento della rilevazione

Le parole chiave vengono cercate sia nelle **ipotesi parziali** sia nelle frasi
definitive. Questo permette di reagire mentre la persona sta ancora parlando,
senza aspettare che finisca la frase, che è il comportamento desiderato in un
contesto di emergenza.

### Perché esiste il cooldown

Vosk emette una trascrizione **ogni 100 ms**. Finché la persona parla, la parola
resta dentro l'ipotesi parziale e viene rilanciata identica decine di volte: un
solo "aiuto" produceva una raffica di righe identiche nello stesso secondo, e di
conseguenza il log degli allarmi era illeggibile e inutilizzabile per qualunque
verifica automatica.

`alert.Cooldown` risolve il problema tenendo traccia dell'ultimo istante in cui
ogni parola chiave ha fatto scattare l'allarme **su ogni telecamera**: entro la
finestra configurata i match successivi della **stessa** parola sulla **stessa**
telecamera vengono scartati (e tracciati a livello `debug`). Una parola chiave
diversa non è influenzata, la stessa parola sentita da un'altra telecamera
produce il proprio allarme, e la parola scattata di nuovo dopo la finestra passa
normalmente.

```json
"cooldown_seconds": 3
```

- `3` (default) — un allarme ogni 3 secondi per parola chiave e telecamera
- `0` — nessun filtro, ogni match emette un allarme (comportamento iniziale)
- valore negativo — rifiutato dalla configurazione, è quasi certamente un errore

Un valore troppo alto rischia di perdere un allarme reale, uno troppo basso
riproduce la raffica. Per una sorveglianza continua 2-5 secondi è un buon
compromesso.

A ogni avvio e a ogni arresto, il programmo scrive cosa ha sentito:

```
INFO avvio help-alarm keywords="[aiuto soccorso emergenza]" telecamere="[ingresso (192.168.1.10) cortile (192.168.1.11)]" modello=... cooldown=3s
INFO telecamere in ascolto numero=2
INFO telecamera connessa camera=ingresso camera_ip=192.168.1.10
WARN stream interrotto, nuovo tentativo camera=cortile camera_ip=192.168.1.11 errore="... Connection refused" attesa=1s
INFO trascrizione camera=ingresso camera_ip=192.168.1.10 testo="per favore aiuto"
INFO arresto in corso
INFO arresto completato
```

e sul terminale, una sola volta per parola chiave e telecamera entro il cooldown:

```
16:10:20 aiuto rilevato da ingresso (192.168.1.10), luce accesa
```

## Arresto

`Ctrl+C` (SIGINT) o `SIGTERM` fermano il programma in modo ordinato: i processi
ffmpeg di tutte le telecamere vengono fermati, per ogni telecamera la frase
eventualmente rimasta a mezz'aria viene trascritta e analizzata, e solo dopo
vengono liberate le risorse native.

## Note tecniche

**Formato audio.** Vosk accetta solo PCM signed 16 bit little endian, mono, a
16 000 Hz. `internal/rtsp` chiede esattamente questo a ffmpeg (`-map 0:a:0 -ac 1
-ar 16000 -f s16le`), che converte qualunque codec audio della telecamera.
Il video non viene decodificato.

**Trasporto RTSP.** ffmpeg usa RTSP su TCP (`-rtsp_transport tcp`): niente
pacchetti persi o riordinati, e funziona anche dove l'UDP è filtrato. Il timeout
di rete è di 10 s.

**Modello linguistico.** `vosk-model-small-it-0.22` è scelto perché gira su
Raspberry Pi e su computer modesti (~300 MB di memoria). Esiste anche
`vosk-model-it-0.22` (1.2 GB, accuratezza quasi doppia ma pensato per server):
basta scaricarlo e cambiare `model_path`.

**Libreria nativa.** I binding Go di Vosk chiamano `libvosk.so` tramite cgo,
quindi servono header e libreria al momento della compilazione. Il `Makefile`
imposta `-I`, `-L` e `-rpath` in modo che il binario trovi la libreria anche a
runtime senza configurare `LD_LIBRARY_PATH`.

## Sviluppo futuro: la luce

Il GPIO non è ancora implementato, come previsto dai requisiti. La base è però
già pronta: `internal/alert` espone l'interfaccia

```go
type Alerter interface {
    Name() string
    Trigger(Event) error
}
```

per ora implementata solo da `TerminalAlerter`. Per accendere una luce basterà
aggiungere una seconda implementazione (con `go-rpio` o `periph.io`) e passarla
al `listener` in `main.go`, senza modificare nessun altro package. Allo stesso
modo, un attivatore che accenda un relè potrà sostituire la `fmt.Fprintf` sul
terminale.

## Test

```bash
make test
```

I test coprono la normalizzazione e il riconoscimento a confine di parola delle
parole chiave, il caricamento e la validazione della configurazione, la
separazione dei due file di log, il comportamento del cooldown (compreso il caso
della raffica da 10 match ravvicinati e il filtro per telecamera), la struttura
del JSON di Vosk e la ricezione degli stream: blocchi da 100 ms, riconnessione,
chiusura ordinata e assenza delle password nei log. I test degli stream non usano
ffmpeg: il processo è sostituito da un finto ffmpeg che emette PCM.

Il riconoscimento della voce in sé si verifica con un server RTSP di prova, per
esempio [MediaMTX](https://github.com/bluenviron/mediamtx), su cui pubblicare
un file audio:

```bash
docker network create ha-test
docker run -d --name mtx --network ha-test bluenviron/mediamtx
docker run -d --network ha-test -v "$PWD":/in:ro --entrypoint ffmpeg help-alarm \
  -re -stream_loop -1 -i /in/aiuto.wav -c:a aac -f rtsp rtsp://mtx:8554/cam1
```

e una telecamera `rtsp://mtx:8554/cam1` in `config.json`, avviando il container
del programma sulla stessa rete (`--network ha-test`).

## Note sui requisiti originali

- Il logging usa `log/slog` della libreria standard, come richiesto.
- L'analisi del testo non usa `strings.Contains` grezzo ma un confronto a
  confine di parola, che evita i falsi positivi; il requisito è rispettato nello
  spirone e reso più robusto.
