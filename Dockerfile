# syntax=docker/dockerfile:1.7

FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS builder

ENV CGO_ENABLED=0 \
    GOFLAGS="-trimpath -buildvcs=false" \
    GOPROXY=https://proxy.golang.org,direct

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /out/watermark-go ./cmd/watermark-go \
    && go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /out/parser-helper ./cmd/parser-helper \
    && go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /out/netguard-proxy ./cmd/netguard-proxy

FROM python:3.12.11-slim-bookworm@sha256:519591d6871b7bc437060736b9f7456b8731f1499a57e22e6c285135ae657bf7 AS runtime

ARG GITHUB_SHA=unknown

LABEL org.opencontainers.image.revision=$GITHUB_SHA \
      org.opencontainers.image.source=https://github.com/1136623363/watermark-go

ENV TZ=Asia/Shanghai \
    PORT=5001 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONHASHSEED=0 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    LOG_DIR=/app/logs \
    YT_DLP_BINARY=/usr/local/bin/yt-dlp \
    FFMPEG_BINARY=/usr/bin/ffmpeg \
    UNIVERSAL_PARSER_BRIDGE_SCRIPT=/app/bridges/universal/python/bridge.py \
    UNIVERSAL_PARSER_VIDEODL_PATH=/app/third_party/CharlesPikachu/videodl \
    UNIVERSAL_PARSER_MUSICDL_PATH=/app/third_party/CharlesPikachu/musicdl \
    UNIVERSAL_PARSER_WORK_DIR=/app/cache/universal-parser \
    UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS=15 \
    UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT=5

WORKDIR /app
COPY requirements.lock /tmp/requirements.lock
ADD --checksum=sha256:b3f0761970d307b210859b6b9a3b530d7b93543479ad300d7ed6fd6a68dc0efa https://github.com/CharlesPikachu/videodl/archive/28c566ed55953ef201956ea30c3274ccfab18c84.tar.gz /tmp/videodl.tar.gz
ADD --checksum=sha256:17268accc270cd2ca7cd4150469078e89dc288bf71d9ccd3fa3555f093ec1e84 https://github.com/CharlesPikachu/musicdl/archive/bd74f2528a0e37854f42ee6dcce344c153229a6e.tar.gz /tmp/musicdl.tar.gz
RUN groupadd --system --gid 10001 watermark \
    && useradd --system --uid 10001 --gid 10001 --home-dir /app --shell /usr/sbin/nologin watermark \
    && mkdir -p /app/bin /app/cache /app/logs /app/tmp /app/tools /app/third_party/CharlesPikachu \
    && printf 'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/20260718T000000Z bookworm main\n' > /etc/apt/sources.list \
    && printf 'Acquire::Check-Valid-Until "false";\nAcquire::Retries "3";\n' > /etc/apt/apt.conf.d/80snapshot \
    && apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg=7:5.1.9-0+deb12u1 ca-certificates \
    && chown -R 10001:10001 /app/cache /app/logs /app/tmp \
    && chmod 0755 /app /app/bin /app/tools /app/third_party /app/third_party/CharlesPikachu \
    && chmod 0700 /app/cache /app/logs /app/tmp \
    && tar -xzf /tmp/videodl.tar.gz -C /app/third_party/CharlesPikachu \
    && tar -xzf /tmp/musicdl.tar.gz -C /app/third_party/CharlesPikachu \
    && mv /app/third_party/CharlesPikachu/videodl-28c566ed55953ef201956ea30c3274ccfab18c84 /app/third_party/CharlesPikachu/videodl \
    && mv /app/third_party/CharlesPikachu/musicdl-bd74f2528a0e37854f42ee6dcce344c153229a6e /app/third_party/CharlesPikachu/musicdl \
    && python -m pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock \
    && rm -rf /var/lib/apt/lists/* /tmp/requirements.lock /tmp/videodl.tar.gz /tmp/musicdl.tar.gz

COPY --from=builder /out/watermark-go /app/bin/watermark-go
COPY --from=builder /out/parser-helper /app/bin/parser-helper
COPY --from=builder /out/netguard-proxy /app/bin/netguard-proxy
COPY bridges /app/bridges

RUN chmod 0555 /app/bin/watermark-go /app/bin/parser-helper /app/bin/netguard-proxy \
    && chmod -R a-w /app/bridges /app/tools /app/third_party

USER 10001:10001
EXPOSE 5001
ENTRYPOINT ["/app/bin/watermark-go"]
CMD ["serve"]
