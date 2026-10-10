# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`help-alarm`: Go program that listens to the audio of N IP cameras over RTSP, transcribes speech fully offline with Vosk (Italian small model), and fires an alert naming the camera when a configured keyword (e.g. "aiuto") is heard. README.md and code comments are in Italian; keep new comments/messages in Italian to match. Go module path is `github.com/giulio/help-alarm-STT`.

## Commands

cgo is required (Vosk). Always go through the `Makefile`, which exports `CGO_CFLAGS`/`CGO_LDFLAGS` pointing at `third_party/vosk` (with rpath). Plain `go build`/`go test` fail without those exports.

```bash
make setup     # ./scripts/setup.sh: downloads libvosk.so, vosk_api.h and the model (once; not in git)
make build     # -> ./help-alarm
make test      # go test ./... (needs neither ffmpeg nor cameras)
make vet / make fmt
```

Single test: `make` exports the flags only for its own recipes, so for `go test` directly, export them first:
```bash
export CGO_CFLAGS="-I$PWD/third_party/vosk" CGO_LDFLAGS="-L$PWD/third_party/vosk -lvosk -Wl,-rpath,$PWD/third_party/vosk"
go test ./internal/keyword -run TestName
```

**ffmpeg exists only in the Docker image, by user decision: never require it on the host.** The program is run via `docker compose up -d` / `make docker-up`; run locally it exits at startup with "ffmpeg non trovato". Local work is build + test only. Logs go to the named volume `help-alarm-stt_logs`, not `./logs`. `config.json` is bind-mounted.

Runtime flags: `-config path`, `-v`/`-verbose`. `cmd/checkvocab` is a throwaway tool that checks whether the model's vocabulary contains candidate keywords (run from repo root).

## Architecture

`main.go` builds one independent pipeline per camera in `config.json` `cameras`: `rtsp` → `stt` → `keyword` → `alert` (a `listener` per camera on its own goroutine; `Matcher`, `TerminalAlerter` and `Cooldown` are shared and concurrency-safe).

- `internal/rtsp`: one ffmpeg subprocess per camera (`-rtsp_transport tcp -map 0:a:0 -ac 1 -ar 16000 -f s16le pipe:1`), read in 100 ms frames (3200 bytes). Non-blocking channel send drops frames if transcription lags. If ffmpeg exits, it is restarted with exponential backoff (1 s to 30 s, reset when audio arrived). Tests replace ffmpeg with the test binary itself (`TestHelperProcess`) via the unexported `command` field.
- `internal/stt`: `LoadModel` once (shared), `NewRecognizer` per camera (recognizers hold per-utterance state, not shareable). Close recognizers before the model. `Accept` returns partial hypotheses or final text; `Flush` is called at shutdown for the pending phrase.
- `internal/keyword`: normalizes text (lowercase, no punctuation) and matches at **word boundaries**, supporting multi-word phrases ("aiutami" does not match "aiuto"). Keywords from config are normalized and deduplicated the same way.
- `internal/alert`: `Alerter` interface (`TerminalAlerter` is the only implementation; the message is "<keyword> rilevato da <camera> (<ip>), luce accesa" — the "light" is not actually driven yet) plus `Cooldown`, a per-(camera, keyword) suppression window. Needed because Vosk emits every 100 ms and the keyword stays in the partial hypothesis, which would otherwise produce bursts of duplicates. Partial hypotheses are deliberately matched too, for fast reaction.
- `internal/config`: loads `config.json`, applies defaults, validates (≥1 camera, unique names, `rtsp://`/`rtsps://` URL with host); relative paths resolve against the config file's directory.
- `internal/logging`: three separate loggers (`Console`, `File`, `Alerts`).

### Privacy invariant

Without `-v` nothing spoken may reach the terminal or disk, except the alert itself. This is enforced inside the logger (Console/File write to a discarding writer when not verbose; the general log file is not even created), not at call sites. Alert records contain only `timestamp`, `keyword`, `camera`, `camera_ip`, never the surrounding phrase (`alert.Event` has no field for it). Preserve this when adding logging. The alert line is printed to the terminal directly with `fmt.Fprintf`, while the alerts logger writes only to its file, to avoid duplicate terminal output.

### Camera credentials

Camera URLs carry `user:password@`. They must never reach logs, terminal or error messages: show only the camera name and `rtsp.Host(url)`. ffmpeg echoes the full URL in its stderr, so anything from ffmpeg goes through `rtsp.Redact`. Config validation errors name the camera, never the URL. Tests guard this (`TestErrorsDoNotLeakPassword`, `TestCameraErrorsHidePassword`).

### Gotchas

- `config.json` has multilingual keywords; the model is Italian-only, so non-Italian keywords may never be recognized (see `cmd/checkvocab`). Its camera URLs are placeholders.
- `models/`, `third_party/vosk/*.so|*.h`, `logs/` and the `help-alarm` binary are git-ignored.
