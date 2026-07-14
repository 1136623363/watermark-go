import re
import uuid

import pytest
import requests

from conftest import assert_api_ok, assert_http_ok, wait_for_download_task


def _fallback_payload(source_url, media_url, share_id):
    return {
        "sourceUrl": source_url,
        "mediaUrl": media_url,
        "mediaType": "video",
        "shareId": share_id,
        "attempt": 4,
    }


@pytest.mark.admin
def test_admin_auth_and_summary(client, admin_client):
    unauthorized = client.get("/admin/api/summary")
    assert unauthorized.response.status_code == 401, unauthorized.response.text
    assert unauthorized.body.get("code") == 401, unauthorized.body

    bad_login = client.post(
        "/admin/login",
        json={"username": "admin", "password": "definitely-wrong"},
    )
    assert bad_login.response.status_code == 401, bad_login.response.text

    summary = admin_client.get("/admin/api/summary")
    data = assert_api_ok(summary)
    assert data["infrastructure"]["mysqlStatus"] == "ok", data
    assert data["infrastructure"]["redisStatus"] == "ok", data
    assert data["settings"]["downloadFallbackMode"] in {"cache", "proxy", "cdn"}, data


@pytest.mark.fallback
def test_download_fallback_rejects_bad_inputs(client, client_session):
    token = client_session["token"]

    invalid_payload = client.post(
        "/api/download/fallback",
        data="not-json",
        headers={"Content-Type": "application/json", "token": token},
    )
    assert invalid_payload.response.status_code == 200
    assert invalid_payload.body.get("code") == 1004, invalid_payload.body

    unsupported_media = client.post(
        "/api/download/fallback",
        json={
            "sourceUrl": "https://v.douyin.com/e2e/",
            "mediaUrl": "https://example.com/file.bin",
            "mediaType": "binary",
            "shareId": "bad-media",
        },
        headers={"token": token},
    )
    assert unsupported_media.response.status_code == 200
    assert unsupported_media.body.get("code") == 1004, unsupported_media.body

    invalid_url = client.post(
        "/api/download/fallback",
        json={
            "sourceUrl": "https://v.douyin.com/e2e/",
            "mediaUrl": "not-a-url",
            "mediaType": "video",
            "shareId": "bad-url",
        },
        headers={"token": token},
    )
    assert invalid_url.response.status_code == 200
    assert invalid_url.body.get("code") == 1004, invalid_url.body


@pytest.mark.e2e
@pytest.mark.fallback
def test_download_fallback_full_flow_records_actual_mode(
    client,
    admin_client,
    client_session,
    media_url,
    source_url,
    download_timeout,
):
    health = assert_api_ok(client.get("/api/health"))
    mode = health["node"]["downloadFallbackMode"]
    share_id = "pytest-" + uuid.uuid4().hex[:10]
    token = client_session["token"]
    uid = str(client_session["uid"])
    assert re.fullmatch(r"\d{8}", uid), client_session

    created = client.post(
        "/api/download/fallback",
        json=_fallback_payload(source_url, media_url, share_id),
        headers={"token": token, "X-Forwarded-For": "203.0.113.20"},
    )
    assert_http_ok(created)

    if mode == "cdn" and created.body.get("code") != 0:
        pytest.fail(f"cdn mode is enabled but fallback creation failed: {created.body}")

    assert created.body.get("code") == 0, created.body
    data = created.body.get("data")

    if isinstance(data, str):
        download_url = data
    else:
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

    records = assert_api_ok(admin_client.get("/admin/api/download-fallback?hours=1&limit=50"))
    assert records["mode"] == mode, records
    assert records["stats"]["byMode"].get(mode, 0) >= 1, records

    recent = [item for item in records["recent"] if item.get("shareId") == share_id]
    assert recent, records["recent"][:5]
    assert any(item.get("mode") == mode for item in recent), recent
    assert any(item.get("uid") == uid for item in recent), recent
    assert all(item.get("uid", uid).isdigit() for item in recent if item.get("uid")), recent
    task_ids = {item.get("taskId") for item in recent if item.get("taskId")}
    assert task_ids, recent
    assert len(task_ids) == 1, recent
