import re
import time

import pytest

from conftest import assert_api_error, assert_api_ok


@pytest.mark.smoke
def test_health_endpoint_is_single_node_shape(client):
    health = client.get("/healthz")
    data = assert_api_ok(health)
    assert data["status"] == "ok", data
    assert "node" not in data, data
    assert ("clu" + "ster") not in data, data


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
    assert_api_error(missing_identity, 1008)


@pytest.mark.smoke
def test_parse_requires_valid_token_and_accepts_token_or_bearer(client, client_session, m3u8_url):
    no_token = client.post("/api/parse", json={"url": m3u8_url, "source": 12})
    assert no_token.response.status_code == 200
    assert_api_error(no_token, 1008)

    bad_token = client.post("/api/parse", json={"url": m3u8_url, "source": 12}, headers={"token": "bad"})
    assert bad_token.response.status_code == 200
    assert_api_error(bad_token, 1008)

    token = client_session["token"]
    for headers in ({"token": token}, {"Authorization": f"Bearer {token}"}):
        parsed = client.post("/api/parse", json={"url": m3u8_url, "source": 12}, headers=headers)
        data = assert_api_ok(parsed)
        assert data["platform"] == "m3u8", data
