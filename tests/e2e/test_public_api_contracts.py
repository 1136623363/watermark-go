import requests
import pytest

from conftest import (
    assert_api_error,
    assert_api_ok,
    assert_http_ok,
    assert_v1_error,
    assert_v1_ok,
    signed_parse_payload,
)


@pytest.mark.smoke
def test_public_pages_and_unsupported_contracts(base_url, client):
    root = requests.get(base_url + "/", allow_redirects=False, timeout=10)
    assert root.status_code == 302
    assert root.headers.get("Location") == "/admin"

    login = requests.get(base_url + "/admin/login", timeout=10)
    assert login.status_code == 200
    assert "text/html" in login.headers.get("content-type", "")

    settings = requests.get(base_url + "/settings", allow_redirects=False, timeout=10)
    assert settings.status_code == 302
    assert settings.headers.get("Location") == "/admin#settings"

    preview = requests.get(
        base_url + "/preview/player",
        params={"title": "pytest", "src": "https://example.com/video.mp4", "poster": "https://example.com/poster.jpg"},
        timeout=10,
    )
    assert preview.status_code == 200
    assert "text/html" in preview.headers.get("content-type", "")

    profile = client.post("/api/profile", json={})
    assert profile.response.status_code == 200
    assert_api_error(profile, 1002)


@pytest.mark.e2e
def test_parse_compatibility_and_cache_endpoints(client, client_session, client_signature_key, m3u8_url):
    hybrid = client.get("/api/hybrid/video_data", params={"url": m3u8_url})
    hybrid_data = assert_api_ok(hybrid)
    assert hybrid_data["platform"] == "m3u8", hybrid_data
    assert hybrid_data["m3u8"] == m3u8_url, hybrid_data

    legacy = client.get("/video/share/url/parse", params={"url": m3u8_url})
    assert_http_ok(legacy)
    assert legacy.body.get("code") == 200, legacy.body
    legacy_data = legacy.body.get("data") or {}
    assert legacy_data["video_url"] == m3u8_url, legacy_data

    token = client_session["token"]
    signed = client.post(
        "/api/parse",
        json=signed_parse_payload(m3u8_url, token, client_signature_key),
        headers={"token": token},
    )
    signed_data = assert_api_ok(signed)
    assert signed_data["platform"] == "m3u8", signed_data
    assert signed_data["shareId"], signed_data

    cached = client.get(f"/api/parse/cache/{signed_data['shareId']}")
    cached_data = assert_api_ok(cached)
    assert cached_data["sourceUrl"] == m3u8_url, cached_data

    missing_cache = client.get("/api/parse/cache/not-a-real-cache-key")
    assert missing_cache.response.status_code == 200
    assert_api_error(missing_cache, 1004)


@pytest.mark.e2e
def test_v1_api_contracts(client):
    platforms = assert_v1_ok(client.get("/api/v1/platforms"))
    assert isinstance(platforms, list) and platforms, platforms
    assert any(item.get("source") == "douyin" for item in platforms), platforms[:5]

    missing_url = client.get("/api/v1/parse")
    assert_v1_error(missing_url, 400, "MISSING_PARAMETER")

    unsupported_url = client.get("/api/v1/parse", params={"url": "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8"})
    assert_v1_error(unsupported_url, 400, "UNSUPPORTED_URL")

    bad_source = client.get("/api/v1/parse/no-such-source/123")
    assert_v1_error(bad_source, 400, "UNSUPPORTED_SOURCE")

    no_id_parser = client.get("/api/v1/parse/xinpianchang/123")
    assert_v1_error(no_id_parser, 400, "ID_PARSE_NOT_SUPPORTED")


@pytest.mark.e2e
def test_legacy_and_m3u8_error_contracts(client):
    legacy_empty = client.get("/video/share/url/parse", params={"url": ""})
    assert legacy_empty.response.status_code == 200
    assert legacy_empty.body.get("code") == 201, legacy_empty.body

    legacy_id = client.get("/video/id/parse", params={"source": "no-such-source", "video_id": "1"})
    assert legacy_id.response.status_code == 200
    assert legacy_id.body.get("code") == 201, legacy_id.body

    hybrid_empty = client.get("/api/hybrid/video_data", params={"url": ""})
    assert hybrid_empty.response.status_code == 200
    assert_api_error(hybrid_empty, 1004)

    merge_bad = client.get("/api/m3u8/merge", params={"url": "not-a-url"})
    assert merge_bad.response.status_code == 200
    assert_api_error(merge_bad, 1004)

    missing_task = client.get("/api/task/not-a-task")
    assert missing_task.response.status_code == 200
    assert_api_error(missing_task, 1004)

    missing_file = client.get("/api/task/file/not-a-task")
    assert missing_file.response.status_code == 404
    assert_api_error(missing_file, 1004)

    internal_platform_test = client.post("/api/internal/platform-test", json={})
    assert internal_platform_test.response.status_code in {403, 404}, internal_platform_test.response.text
    if internal_platform_test.body:
        assert_api_error(internal_platform_test)


@pytest.mark.e2e
def test_download_fallback_status_file_and_node_error_contracts(client):
    bad_status = client.get("/api/download/status/bad-ticket")
    assert bad_status.response.status_code == 200
    assert_api_error(bad_status, 1004)

    bad_proxy = client.get("/api/download/proxy/bad-ticket")
    assert bad_proxy.response.status_code == 403
    assert_api_error(bad_proxy, 1004)

    bad_cdn = client.get("/api/download/cdn/bad-ticket")
    assert bad_cdn.response.status_code == 403
    assert_api_error(bad_cdn, 1004)

    bad_file = client.get("/api/download/file/not-a-key")
    assert bad_file.response.status_code == 403
    assert_api_error(bad_file, 1004)

    bad_task = client.get("/api/download/fallback/not-a-task")
    assert bad_task.response.status_code == 200
    assert_api_error(bad_task, 1004)

    unknown_node_status = client.get("/api/download/node/no-such-node/fallback/not-a-task")
    assert unknown_node_status.response.status_code == 200
    assert_api_error(unknown_node_status, 1004)

    unknown_node_file = client.get("/api/download/node/no-such-node/file/not-a-key")
    assert unknown_node_file.response.status_code == 200
    assert_api_error(unknown_node_file, 1004)

    internal_status = client.get("/api/internal/download/fallback/not-a-task")
    assert internal_status.response.status_code in {403, 404}, internal_status.response.text
    if internal_status.body:
        assert_api_error(internal_status)
