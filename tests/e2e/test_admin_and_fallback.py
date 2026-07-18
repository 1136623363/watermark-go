import requests

import pytest

from conftest import assert_api_error, assert_api_ok, assert_http_ok, wait_for_download_task


def _fallback_payload(media_url):
    return {
        "mediaUrl": media_url,
        "mediaType": "video",
        "attempt": 4,
    }


@pytest.mark.admin
def test_admin_auth_and_summary(client, admin_client):
    unauthorized = client.get("/admin/api/summary")
    assert unauthorized.response.status_code == 401, unauthorized.response.text
    assert unauthorized.body.get("code") == 401, unauthorized.body

    bad_login = client.post(
        "/admin/api/login",
        json={"username": "admin", "password": "definitely-wrong"},
    )
    assert bad_login.response.status_code == 401, bad_login.response.text

    summary = admin_client.get("/admin/api/summary")
    data = assert_api_ok(summary)
    assert data["uptimeSeconds"] >= 0, data
    assert "platformCount" in data, data


@pytest.mark.fallback
def test_download_fallback_rejects_bad_inputs(client):
    invalid_payload = client.post(
        "/api/download/fallback",
        data="not-json",
        headers={"Content-Type": "application/json"},
    )
    assert invalid_payload.response.status_code == 200
    assert invalid_payload.body.get("code") == 1004, invalid_payload.body

    too_early = client.post(
        "/api/download/fallback",
        json={"mediaUrl": "https://example.com/file.mp4", "mediaType": "video", "attempt": 1},
    )
    assert too_early.response.status_code == 200
    assert too_early.body.get("code") == 1004, too_early.body

    invalid_url = client.post(
        "/api/download/fallback",
        json={"mediaUrl": "not-a-url", "mediaType": "video", "attempt": 4},
    )
    assert invalid_url.response.status_code == 200
    assert invalid_url.body.get("code") == 1004, invalid_url.body


@pytest.mark.e2e
@pytest.mark.fallback
def test_download_fallback_full_flow(client, media_url, download_timeout):
    created = client.post("/api/download/fallback", json=_fallback_payload(media_url))
    assert_http_ok(created)
    data = assert_api_ok(created)

    completed = wait_for_download_task(client, data, timeout_seconds=download_timeout)
    download_url = completed.get("downloadUrl") or completed.get("url")
    assert isinstance(download_url, str) and download_url.startswith(("http://", "https://", "/")), download_url

    download_response = requests.get(
        client.url(download_url) if download_url.startswith("/") else download_url,
        headers={"Range": "bytes=0-1023"},
        timeout=download_timeout,
        stream=True,
    )
    try:
        assert download_response.status_code in {200, 206}, download_response.text[:300]
        chunk = next(download_response.iter_content(chunk_size=256), b"")
        assert chunk, "download response should contain media bytes"
    finally:
        download_response.close()
