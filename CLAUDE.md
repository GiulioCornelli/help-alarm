# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`help-alarm`: Go program that listens to the microphone, transcribes speech fully offline with Vosk (Italian small model), and fires an alert when a configured keyword (e.g. "aiuto") is heard. README.md and code comments are in Italian; keep new comments/messages in Italian to match. Go module path is `github.com/giulio/help-alarm-STT`.

## Commands

cgo is required (malgo + Vosk). Always go through the `Makefile`, which exports `CGO_CFLAGS`/`CGO_LDFLAGS` pointing at `third_party/vosk` (with rpath). Plain `go build`/`go test` fail without those exports.

```bash
make setup     # ./scripts/setup.sh: downloads libvosk.so, vosk_api.h and the model (once; not in git)
make build     # -> ./help-alarm
make run       # build + run with config.json
make devices   # list capture devices (for device_id in config.json)
make test      # go test ./...
make vet / make fmt
```

Single test: `make` exports the flags only for its own recipes, so for `go test` directly, export them first:
```bash
export CGO_CFLAGS="-I$PWD/third_party/vosk" CGO_LDFLAGS="-L$PWD/third_party/vosk -lvosk -Wl,-rpath,$PWD/third_party/vosk"
go test ./internal/keyword -run TestName
```

Docker path (no local deps): `docker compose up -d` / `make docker-up`. Logs go to the named volume `help-alarm-stt_logs`, not `./logs`. `config.json` is bind-mounted.

Runtime flags: `-config path`, `-devices`, `-v`/`-verbose`. `cmd/checkvocab` is a throwaway tool that checks whether the model's vocabulary contains candidate keywords (run from repo root).

## Architecture

`main.go` wires a pipeline: `audio` → `stt` → `keyword` → `alert`, with a goroutine and channel between capture and transcription.

- `internal/audio`: malgo/miniaudio capture, 16 kHz mono PCM s16. The capture callback runs on the audio thread and must never block: it copies the buffer and does a non-blocking `select` send, dropping frames if the consumer lags.
- `internal/stt`: Vosk wrapper. `Accept` returns partial hypotheses or final text; `Flush` is called at shutdown for the pending phrase.
- `internal/keyword`: normalizes text (lowercase, no punctuation) and matches at **word boundaries**, supporting multi-word phrases ("aiutami" does not match "aiuto"). Keywords from config are normalized and deduplicated the same way.
- `internal/alert`: `Alerter` interface (`TerminalAlerter` is the only implementation; the message is "<keyword> rilevato, luce accesa" — the "light" is not actually driven yet) plus `Cooldown`, a per-keyword suppression window. Needed because Vosk emits every 100 ms and the keyword stays in the partial hypothesis, which would otherwise produce bursts of duplicates. Partial hypotheses are deliberately matched too, for fast reaction.
- `internal/config`: loads `config.json`, applies defaults, validates; relative paths resolve against the config file's directory.
- `internal/logging`: three separate loggers (`Console`, `File`, `Alerts`).

### Privacy invariant

Without `-v` nothing spoken may reach the terminal or disk, except the alert itself. This is enforced inside the logger (Console/File write to a discarding writer when not verbose; the general log file is not even created), not at call sites. Alert records contain only `timestamp` and `keyword`, never the surrounding phrase (`alert.Event` has no field for it). Preserve this when adding logging. The alert line is printed to the terminal directly with `fmt.Fprintf`, while the alerts logger writes only to its file, to avoid duplicate terminal output.

### Gotchas

- `config.json` currently has duplicated keywords (e.g. `aiutami`) and multilingual entries; the model is Italian-only, so non-Italian keywords may never be recognized (see `cmd/checkvocab`).
- `models/`, `third_party/vosk/*.so|*.h`, `logs/` and the `help-alarm` binary are git-ignored.
