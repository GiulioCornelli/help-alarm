# Build di help-alarm.
#
# I binding Go di Vosk chiamano la libreria nativa C tramite cgo, quindi
# servono i flag di compilazione e linkaggio. La libreria vive dentro il
# progetto (third_party/vosk), perciò non serve installare nulla a sistema.

MODULE      := github.com/giulio/help-alarm-STT
VOSK_DIR    := $(CURDIR)/third_party/vosk

export CGO_ENABLED := 1

BIN         := help-alarm
CGO_CFLAGS  := -I$(VOSK_DIR)
# -rpath evita di dover esportare LD_LIBRARY_PATH a ogni avvio.
CGO_LDFLAGS := -L$(VOSK_DIR) -lvosk -Wl,-rpath,$(VOSK_DIR)

SETUP_CMD  := ./scripts/setup.sh
RM_BIN     := rm -f $(BIN)

export CGO_CFLAGS
export CGO_LDFLAGS

.PHONY: all setup build run devices test fmt vet clean \
	docker-build docker-up docker-down docker-logs

all: build

setup:
	$(SETUP_CMD)

build:
	@test -f $(VOSK_DIR)/vosk_api.h || { echo "libreria Vosk mancante: esegui 'make setup'"; exit 1; }
	go build -trimpath -ldflags "-s -w" -o $(BIN) .
	@echo "[ok] eseguibile in $(BIN)"

run: build
	./$(BIN) -config config.json

devices: build
	./$(BIN) -devices

test:
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	$(RM_BIN)

# --- Docker -----------------------------------------------------------------
# Percorso senza dipendenze locali: Go, gcc, libreria Vosk e modello sono
# dentro l'immagine. Serve solo Docker.

docker-build:
	docker compose build

docker-up:
	docker compose up -d
	docker compose logs -f

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
