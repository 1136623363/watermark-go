import pytest

from conftest import assert_api_error, assert_api_ok, assert_http_ok, assert_v1_error, assert_v1_ok


@pytest.mark.smoke
def test_profile_is_explicitly_unsupported(client):
    profile = client.get("/api/profile")
    assert profile.response.status_code == 200
    assert_api_error(profile, 1002)


@pytest.mark.e2e
def test_parse_compatibility_and_cache_endpoints(client, client_session, m3u8_url):
    hybrid = client.get("/api/hybrid/video_data", params={"url": m3u8_url})
    hybrid_data = assert_api_ok(hybrid)
    assert hybrid_data["platform"] == "m3u8", hybrid_data
    assert hybrid_data["m3u8"] == m3u8_url, hybrid_data

    legacy = client.get("/video/share/url/parse", params={"url": m3u8_url})
    assert_http_ok(legacy)
    assert legacy.body.get("code") == 200, legacy.body
    legacy_data = legacy.body.get("data") or {}
    assert legacy_data["playAddr"] == m3u8_url, legacy_data

    token = client_session["token"]
    parsed = client.post("/api/parse", json={"url": m3u8_url, "source": 12}, headers={"token": token})
    parsed_data = assert_api_ok(parsed)
    assert parsed_data["platform"] == "m3u8", parsed_data
    assert parsed_data["shareId"], parsed_data

    cached = client.get(f"/api/parse/cache/{parsed_data['shareId']}")
    cached_data = assert_api_ok(cached)
    assert cached_data["sourceUrl"] == m3u8_url, cached_data

    missing_cache = client.get("/api/parse/cache/not-a-real-cache-key")
    assert missing_cache.response.status_code == 200
    assert_api_error(missing_cache, 1004)


@pytest.mark.e2e
def test_v1_parse_contracts(client, m3u8_url):
    missing_url = client.get("/api/v1/parse")
    assert_v1_error(missing_url, 400, "MISSING_PARAMETER")

    parsed = assert_v1_ok(client.get("/api/v1/parse", params={"url": m3u8_url}))
    assert parsed["platform"] == "m3u8", parsed

    bad_source = client.get("/api/v1/parse/no-such-source/123")
    assert_v1_error(bad_source, 400, "UNSUPPORTED_URL")


@pytest.mark.e2e
def test_legacy_m3u8_and_removed_route_error_contracts(client):
    legacy_empty = client.get("/video/share/url/parse", params={"url": ""})
    assert legacy_empty.response.status_code == 200
    assert legacy_empty.body.get("code") == 201, legacy_empty.body

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
    assert missing_file.response.status_code == 403
    assert_api_error(missing_file, 1008)

    removed_download_route = client.get("/api/download/node/no-such-node/file/not-a-key")
    assert removed_download_route.response.status_code == 404, removed_download_route.response.text

    internal_status = client.get("/api/internal/download/fallback/not-a-task")
    assert internal_status.response.status_code in {403, 404}, internal_status.response.text
    if internal_status.body:
        assert_api_error(internal_status)
