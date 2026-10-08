# help-alarm: tutto ciò che serve è dentro l'immagine.
#   build   -> compila il binario con cgo (Go + gcc + libreria nativa Vosk)
#   runtime -> binario, libreria e modello linguistico, nient'altro
#
# L'audio non viene "installato": i dispositivi del microfono arrivano dal
# docker-compose con --device /dev/snd.

# ---------------------------------------------------------------------------
# deps: scarica la libreria nativa Vosk e il modello linguistico italiano.
# Layer separato così non si riscarica a ogni modifica del codice.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS deps

ARG VOSK_VERSION=0.3.45
ARG MODEL_NAME=vosk-model-small-it-0.22

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl unzip \
	&& rm -rf /var/lib/apt/lists/*

# TARGETARCH è fornito da BuildKit: permette lo stesso Dockerfile su amd64
# (PC) e arm64 (Raspberry Pi).
RUN set -eux; \
	case "${TARGETARCH:-amd64}" in \
	amd64) asset="vosk-${VOSK_VERSION}-py3-none-linux_x86_64.whl" ;; \
	arm64) asset="vosk-${VOSK_VERSION}-py3-none-manylinux2014_aarch64.whl" ;; \
	*) echo "architettura ${TARGETARCH} non supportata" >&2; exit 1 ;; \
	esac; \
	curl -fsSL -o /tmp/vosk.whl \
		"https://github.com/alphacep/vosk-api/releases/download/v${VOSK_VERSION}/${asset}"; \
	curl -fsSL -o /tmp/header.zip \
		"https://github.com/alphacep/vosk-api/releases/download/v${VOSK_VERSION}/vosk-linux-x86-${VOSK_VERSION}.zip"; \
	unzip -q /tmp/vosk.whl -d /tmp/wheel; \
	unzip -q /tmp/header.zip -d /tmp/header; \
	mkdir -p /deps/lib; \
	cp /tmp/wheel/vosk/libvosk.so /deps/lib/; \
	cp /tmp/header/*/vosk_api.h /deps/; \
	curl -fsSL -o /tmp/model.zip "https://alphacephei.com/vosk/models/${MODEL_NAME}.zip"; \
	unzip -q /tmp/model.zip -d /deps; \
	mv "/deps/${MODEL_NAME}" /deps/model; \
	rm -rf /tmp

# ---------------------------------------------------------------------------
# build: compila il binario.
# ---------------------------------------------------------------------------
FROM golang:1.23-bookworm AS build

RUN apt-get update \
	&& apt-get install -y --no-install-recommends gcc libc6-dev \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Gli header servono a cgo: devono essere presenti prima della compilazione.
COPY --from=deps /deps/vosk_api.h /deps/lib/libvosk.so /src/third_party/vosk/

# I sorgenti sono copiati dopo: una modifica al codice non invalida i layer
# di download e di librerie.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -rpath punta al percorso definitivo della libreria nell'immagine finale, così
# il binario la trova a runtime senza LD_LIBRARY_PATH.
ENV CGO_CFLAGS=-I/src/third_party/vosk
ENV CGO_LDFLAGS="-L/src/third_party/vosk -lvosk -Wl,-rpath,/app/lib"

RUN go build -trimpath -ldflags="-s -w" -o /out/help-alarm .

# ---------------------------------------------------------------------------
# runtime: solo il necessario per eseguire il programma.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

RUN apt-get update \
	&& apt-get install -y --no-install-recommends libstdc++6 libasound2 \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --system --uid 1000 --create-home --shell /usr/sbin/nologin helpalarm

COPY --from=build /out/help-alarm /app/help-alarm
COPY --from=deps /deps/lib/libvosk.so /app/lib/libvosk.so
COPY --from=deps /deps/model /app/models/vosk-model-small-it-0.22

# I log devono essere scrivibili: la cartella viene montata dall'host.
RUN mkdir -p /app/logs && chown -R helpalarm:helpalarm /app/logs

USER helpalarm
WORKDIR /app

ENTRYPOINT ["/app/help-alarm"]
CMD ["-config", "/app/config.json"]
