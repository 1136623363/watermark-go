#!/usr/bin/env python3
import builtins
import contextlib
import json
import os
import sys
import urllib.parse
from pathlib import Path


MUSIC_SOURCE_BY_HOST = {
    "music.apple.com": "AppleMusicClient",
    "h5app.kuwo.cn": "BodianMusicClient",
    "www.deezer.com": "DeezerMusicClient",
    "5sing.kugou.com": "FiveSingMusicClient",
    "freemusicarchive.org": "FMAMusicClient",
    "www.jamendo.com": "JamendoMusicClient",
    "www.jiosaavn.com": "JioSaavnMusicClient",
    "www.joox.com": "JooxMusicClient",
    "www.kugou.com": "KugouMusicClient",
    "www.kuwo.cn": "KuwoMusicClient",
    "music.kuwo.cn": "KuwoMusicClient",
    "music.migu.cn": "MiguMusicClient",
    "music.163.com": "NeteaseMusicClient",
    "music.91q.com": "QianqianMusicClient",
    "open.qobuz.com": "QobuzMusicClient",
    "www.qobuz.com": "QobuzMusicClient",
    "y.qq.com": "QQMusicClient",
    "qishui.douyin.com": "SodaMusicClient",
    "soundcloud.com": "SoundCloudMusicClient",
    "open.spotify.com": "SpotifyMusicClient",
    "www.streetvoice.cn": "StreetVoiceMusicClient",
    "tidal.com": "TIDALMusicClient",
}


def project_root() -> Path:
    return Path(__file__).resolve().parents[3]


def add_import_path(env_name: str, fallback: Path):
    raw = os.environ.get(env_name) or str(fallback)
    if not raw:
        return None
    path = Path(raw).resolve()
    if path.exists():
        sys.path.insert(0, str(path))
    return path


def read_payload() -> dict:
    raw = sys.stdin.read().lstrip("\ufeff")
    starts = [idx for idx in (raw.find("{"), raw.find("[")) if idx >= 0]
    if starts:
        raw = raw[min(starts):]
    if not raw.strip():
        return {}
    return json.loads(raw)


def safe_json_value(value):
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    if isinstance(value, dict):
        return {str(k): safe_json_value(v) for k, v in value.items()}
    if isinstance(value, (list, tuple, set)):
        return [safe_json_value(v) for v in value]
    return str(value)


def merge_dict(base, override):
    result = dict(base or {})
    for key, value in (override or {}).items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge_dict(result[key], value)
        else:
            result[key] = value
    return result


def normalize_requests_overrides(value):
    if not isinstance(value, dict):
        return {}
    result = dict(value)
    timeout = result.get("timeout")
    if isinstance(timeout, list):
        result["timeout"] = tuple(timeout)
    return result


def read_musicdl_config(payload):
    raw = payload.get("musicConfigJson") or os.environ.get("MUSICDL_CONFIG_JSON") or ""
    if not str(raw).strip():
        return {}
    parsed = json.loads(str(raw))
    if not isinstance(parsed, dict):
        return {}
    if "sources" in parsed or "requests_overrides" in parsed:
        return parsed
    return {"sources": parsed}


def music_item_limit(payload):
    raw = payload.get("musicItemLimit") or payload.get("limit") or os.environ.get("MUSICDL_ITEM_LIMIT") or 5
    try:
        limit = int(raw)
    except Exception:
        limit = 5
    if limit <= 0:
        return 5
    return min(limit, 20)


def limit_items(items, payload):
    limit = int(payload.get("limit") or 10)
    if limit <= 0:
        limit = 10
    return list(items)[:limit]


@contextlib.contextmanager
def limited_playlist_enumerate(max_items):
    """Limit musicdl playlist loops without patching the upstream source tree."""
    if max_items <= 0:
        yield
        return
    original_enumerate = builtins.enumerate

    def replacement(iterable, start=0):
        try:
            if isinstance(iterable, list) and len(iterable) > max_items:
                probe = iterable[:max_items]
                if all(isinstance(item, dict) for item in probe):
                    return original_enumerate(iterable[:max_items], start)
        except Exception:
            pass
        return original_enumerate(iterable, start)

    builtins.enumerate = replacement
    try:
        yield
    finally:
        builtins.enumerate = original_enumerate


def emit_ok(kind: str, items, meta=None):
    print(json.dumps({
        "ok": True,
        "kind": kind,
        "items": safe_json_value(items),
        "meta": safe_json_value(meta or {}),
    }, ensure_ascii=False))


def emit_error(message: str):
    print(json.dumps({
        "ok": False,
        "error": message,
        "items": [],
    }, ensure_ascii=False))


def video_item_to_dict(item) -> dict:
    data = item.todict() if hasattr(item, "todict") else dict(item)
    return {
        "source": data.get("source"),
        "title": data.get("title"),
        "cover_url": data.get("cover_url"),
        "download_url": data.get("download_url"),
        "audio_download_url": data.get("audio_download_url"),
        "ext": data.get("ext"),
        "identifier": data.get("identifier"),
        "err_msg": data.get("err_msg"),
        "duration": data.get("duration") or data.get("duration_s") or 0,
    }


def song_item_to_dict(item) -> dict:
    data = item.todict() if hasattr(item, "todict") else dict(item)
    return {
        "source": data.get("source"),
        "root_source": data.get("root_source"),
        "song_name": data.get("song_name"),
        "singers": data.get("singers"),
        "album": data.get("album"),
        "cover_url": data.get("cover_url"),
        "download_url": data.get("download_url"),
        "ext": data.get("ext"),
        "identifier": data.get("identifier"),
        "duration_s": data.get("duration_s"),
        "duration": data.get("duration"),
        "lyric": data.get("lyric"),
        "file_size": data.get("file_size"),
        "file_size_bytes": data.get("file_size_bytes"),
        "episodes": data.get("episodes"),
    }


def music_sources_for_url(raw_url):
    parsed = urllib.parse.urlparse(raw_url)
    host = (parsed.hostname or "").lower()
    path = (parsed.path or "").lower()
    if host == "www.bilibili.com" and path.startswith("/audio"):
        return ["BilibiliMusicClient"]
    if host in MUSIC_SOURCE_BY_HOST:
        return [MUSIC_SOURCE_BY_HOST[host]]
    for suffix, source in MUSIC_SOURCE_BY_HOST.items():
        if host.endswith("." + suffix):
            return [source]
    return []


def build_music_client(payload, url=""):
    add_import_path("MUSICDL_PATH", project_root() / "upstreams" / "musicdl")
    from musicdl.musicdl import DEFAULT_MUSIC_SOURCES, MusicClient

    music_config = read_musicdl_config(payload)
    sources = payload.get("sources") or music_sources_for_url(url) or DEFAULT_MUSIC_SOURCES
    work_dir = os.environ.get("BRIDGE_WORK_DIR") or str(project_root() / "runtime")
    per_source_cfg = music_config.get("sources") if isinstance(music_config.get("sources"), dict) else {}
    init_cfg = {}
    for source in sources:
        init_cfg[source] = merge_dict({"work_dir": str(Path(work_dir) / source)}, per_source_cfg.get(source, {}))

    request_override = normalize_requests_overrides(music_config.get("requests_overrides"))
    request_override = merge_dict(request_override, normalize_requests_overrides(payload.get("requestOverride")))
    request_override.setdefault("timeout", (5, 10))
    return MusicClient(music_sources=sources, init_music_clients_cfg=init_cfg, requests_overrides=request_override), sources


def parse_video(payload: dict):
    add_import_path("VIDEODL_PATH", project_root() / "upstreams" / "videodl")
    from videodl.videodl import VideoClient

    url = (payload.get("url") or "").strip()
    if not url:
        raise ValueError("missing url")

    work_dir = os.environ.get("BRIDGE_WORK_DIR") or str(project_root() / "runtime")
    sources = payload.get("sources") or []
    request_override = payload.get("requestOverride") or {}
    client = VideoClient(
        allowed_video_sources=sources,
        init_video_clients_cfg={"WebMediaGrabber": {"work_dir": work_dir}},
        requests_overrides=request_override if isinstance(request_override, dict) else {},
        apply_common_video_clients_only=bool(payload.get("commonOnly")),
    )
    with contextlib.redirect_stdout(sys.stderr):
        infos = client.parsefromurl(url)
    emit_ok("video", [video_item_to_dict(item) for item in limit_items(infos or [], payload)])


def search_music(payload: dict):
    keyword = (payload.get("keyword") or payload.get("url") or "").strip()
    if not keyword:
        raise ValueError("missing keyword")

    client, sources = build_music_client(payload)
    with contextlib.redirect_stdout(sys.stderr):
        result = client.search(keyword)
    flattened = []
    for source_items in (result or {}).values():
        flattened.extend(source_items or [])
    emit_ok("music", [song_item_to_dict(item) for item in limit_items(flattened, payload)], {"sources": sources})


def parse_music_playlist(payload: dict):
    url = (payload.get("url") or "").strip()
    if not url:
        raise ValueError("missing url")

    item_limit = music_item_limit(payload)
    client, sources = build_music_client(payload, url)
    with contextlib.redirect_stdout(sys.stderr), limited_playlist_enumerate(item_limit):
        infos = client.parseplaylist(url)
    emit_ok(
        "music",
        [song_item_to_dict(item) for item in limit_items(infos or [], payload)],
        {"sources": sources, "itemLimit": item_limit},
    )


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    payload = read_payload()
    try:
        if mode == "video":
            parse_video(payload)
        elif mode == "music-search":
            search_music(payload)
        elif mode == "music-playlist":
            parse_music_playlist(payload)
        else:
            raise ValueError(f"unknown bridge mode: {mode}")
    except Exception as exc:
        emit_error(str(exc))


if __name__ == "__main__":
    main()
