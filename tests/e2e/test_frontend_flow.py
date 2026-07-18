import pytest

from conftest import assert_api_ok, wait_for_download_task


@pytest.mark.e2e
def test_current_frontend_flow_without_implicit_tokens(client, client_session, m3u8_url, media_url, download_timeout):
    token = client_session["token"]

    parsed = client.post("/api/parse", json={"url": m3u8_url, "source": 12}, headers={"Authorization": f"Bearer {token}"})
    parsed_data = assert_api_ok(parsed)
    assert parsed_data["platform"] == "m3u8", parsed_data

    task = client.post("/api/parse/task", json={"url": m3u8_url, "source": 12})
    task_data = assert_api_ok(task)
    assert task_data["taskId"] and task_data["pollUrl"], task_data
    poll = client.get(task_data["pollUrl"])
    assert poll.response.status_code == 200, poll.response.text
    assert poll.body.get("code") in {0, 1004}, poll.body

    fallback = client.post("/api/download/fallback", json={"mediaUrl": media_url, "mediaType": "video", "attempt": 4})
    fallback_data = assert_api_ok(fallback)
    completed = wait_for_download_task(client, fallback_data, timeout_seconds=download_timeout)
    assert completed.get("downloadUrl") or completed.get("url"), completed

    m3u8 = client.get("/api/m3u8/merge", params={"url": m3u8_url})
    m3u8_data = assert_api_ok(m3u8)
    assert m3u8_data["taskId"] and m3u8_data["pollUrl"], m3u8_data

    perf = client.post("/api/client/performance", json={"name": "frontend-flow", "duration": 1})
    assert_api_ok(perf)
