FROM golang:1.26-alpine AS builder

ENV CGO_ENABLED=0
ENV GOPROXY=https://goproxy.cn,direct

RUN apk update --no-cache && apk add --no-cache tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /app/watermark-backend ./cmd/watermark-backend

FROM python:3.12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata ffmpeg git build-essential \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir yt-dlp

ENV TZ=Asia/Shanghai
ENV PORT=5001
ENV LOG_DIR=/app/logs
ENV YT_DLP_BINARY=/usr/local/bin/yt-dlp
ENV FFMPEG_BINARY=/usr/bin/ffmpeg
ENV UNIVERSAL_PARSER_BRIDGE_SCRIPT=/app/bridges/universal/python/bridge.py
ENV UNIVERSAL_PARSER_VIDEODL_PATH=/app/third_party/CharlesPikachu/videodl
ENV UNIVERSAL_PARSER_MUSICDL_PATH=/app/third_party/CharlesPikachu/musicdl
ENV UNIVERSAL_PARSER_WORK_DIR=/app/cache/universal-parser
ENV UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS=15
ENV UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT=5
ENV TOOL_UPDATES_ROOT=/app/tools

WORKDIR /app
RUN git clone --depth 1 https://github.com/CharlesPikachu/videodl.git /app/third_party/CharlesPikachu/videodl \
    && git clone --depth 1 https://github.com/CharlesPikachu/musicdl.git /app/third_party/CharlesPikachu/musicdl \
    && pip install --no-cache-dir -r /app/third_party/CharlesPikachu/videodl/requirements.txt \
    && pip install --no-cache-dir -r /app/third_party/CharlesPikachu/musicdl/requirements.txt \
    && mkdir -p /app/cache /app/logs /app/cache/universal-parser /app/tools /tmp/watermark-backend
COPY --from=builder /app/watermark-backend /app/watermark-backend
COPY bridges /app/bridges

EXPOSE 5001

CMD ["./watermark-backend"]
