import re
import time

import pytest

from conftest import assert_api_ok, assert_http_ok


@pytest.mark.smoke
def test_health_endpoints(client):
    health = client.get("/api/health")
    data = assert_api_ok(health)
    assert data["infrastructure"]["mysqlStatus"] == "ok", data
    assert data["infrastructure"]["redisStatus"] == "ok", data
    assert data["node"]["id"], data
    assert data["node"]["downloadFallbackMode"] in {"cache", "proxy", "cdn"}, data

    v1 = client.get("/api/v1/health")
    assert_http_ok(v1)
    assert v1.body.get("status") == "success", v1.body
    assert v1.body.get("data", {}).get("status") == "ok", v1.body


@pytest.mark.smoke
def test_client_session_uid_is_stable_eight_digit_number(client):
    client_id = f"pytest_uid_{int(time.time() * 1000)}"
    payload = {"code": "", "programType": 12, "clientId": client_id}

    first = assert_api_ok(client.post("/api/client/session", json=payload))
    second = assert_api_ok(client.post("/api/client/session", json=payload))

    assert first["userId"] == second["userId"], (first, second)
    assert first["uid"] == second["uid"], (first, second)
    assert re.fullmatch(r"\d{8}", str(first["uid"])), first
    assert first["token"] and second["token"], (first, second)
    assert first["publicId"] != first["uid"], first


@pytest.mark.smoke
def test_client_session_rejects_invalid_payload(client):
    invalid_json = client.post(
        "/api/client/session",
        data="not-json",
        headers={"Content-Type": "application/json"},
    )
    assert invalid_json.response.status_code == 200
    assert invalid_json.body.get("code") == 1004, invalid_json.body

    missing_identity = client.post("/api/client/session", json={"programType": 12})
    assert missing_identity.response.status_code == 200
    assert missing_identity.body.get("code") == 1004, missing_identity.body


@pytest.mark.smoke
def test_signed_parse_rejects_missing_and_invalid_credentials(client, client_session):
    no_token = client.post("/api/parse", json={"url": "https://example.com", "source": 12})
    assert no_token.response.status_code == 200
    assert no_token.body.get("code") == 1008, no_token.body

    token = client_session["token"]
    missing_signature = client.post(
        "/api/parse",
        json={"url": "https://example.com", "source": 12, "timestamp": int(time.time())},
        headers={"token": token},
    )
    assert missing_signature.response.status_code == 200
    assert missing_signature.body.get("code") == 1009, missing_signature.body

    stale_timestamp = client.post(
        "/api/parse",
        json={"url": "https://example.com", "source": 12, "timestamp": 1, "signature": "bad"},
        headers={"token": token},
    )
    assert stale_timestamp.response.status_code == 200
    assert stale_timestamp.body.get("code") == 1010, stale_timestamp.body
