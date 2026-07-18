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
    FFMPEG_BINARY=/usr/local/bin/ffmpeg \
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
ADD --checksum=sha256:a34cf29a2d0addbff273ff85e571a1a88f84d99fb6568506fa3845f73425aab2 https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-07-17-13-22/ffmpeg-N-125649-g8d394252d8-linux64-lgpl.tar.xz /tmp/ffmpeg.tar.xz
RUN <<'SH'
set -e
groupadd --system --gid 10001 watermark
useradd --system --uid 10001 --gid 10001 --home-dir /app --shell /usr/sbin/nologin watermark
mkdir -p /app/bin /app/cache /app/logs /app/tmp /app/tools /app/third_party/CharlesPikachu /opt/ffmpeg
chown -R 10001:10001 /app/cache /app/logs /app/tmp
chmod 0755 /app /app/bin /app/tools /app/third_party /app/third_party/CharlesPikachu
chmod 0700 /app/cache /app/logs /app/tmp
python - <<'PY'
import shutil
import tarfile
from pathlib import Path

third_party = Path("/app/third_party/CharlesPikachu")

def extract_package(archive_path, source_root, package_name, destination_name):
    destination = third_party / destination_name
    with tarfile.open(archive_path) as archive:
        for member in archive.getmembers():
            prefix = source_root + "/"
            if not member.name.startswith(prefix):
                continue
            relative = member.name[len(prefix):]
            if relative == "LICENSE":
                relative = "LICENSE"
            elif not relative.startswith(package_name + "/"):
                continue
            target = destination / relative
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(member) as source, target.open("wb") as output:
                    shutil.copyfileobj(source, output)

extract_package(
    "/tmp/videodl.tar.gz",
    "videodl-28c566ed55953ef201956ea30c3274ccfab18c84",
    "videodl",
    "videodl",
)
extract_package(
    "/tmp/musicdl.tar.gz",
    "musicdl-bd74f2528a0e37854f42ee6dcce344c153229a6e",
    "musicdl",
    "musicdl",
)
with tarfile.open("/tmp/ffmpeg.tar.xz") as archive:
    for member in archive.getmembers():
        if not member.isfile():
            continue
        relative = "/".join(member.name.split("/")[1:])
        if relative not in {"bin/ffmpeg", "bin/ffprobe", "LICENSE.txt"}:
            continue
        target = Path("/opt/ffmpeg") / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        with archive.extractfile(member) as source, target.open("wb") as output:
            shutil.copyfileobj(source, output)
Path("/usr/local/bin/ffmpeg").symlink_to("/opt/ffmpeg/bin/ffmpeg")
Path("/usr/local/bin/ffprobe").symlink_to("/opt/ffmpeg/bin/ffprobe")
PY
python -m pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock
chmod 0555 /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe
rm -rf /tmp/requirements.lock /tmp/videodl.tar.gz /tmp/musicdl.tar.gz /tmp/ffmpeg.tar.xz
SH

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
