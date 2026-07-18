import pytest

from conftest import assert_api_error, assert_api_ok


@pytest.mark.admin
@pytest.mark.contract
@pytest.mark.parametrize(
    ("method", "path", "kwargs"),
    [
        ("GET", "/admin/api/summary", {}),
        ("POST", "/admin/api/settings", {"json": {}}),
    ],
)
def test_admin_api_requires_session(client, method, path, kwargs):
    result = client.request(method, path, **kwargs)
    assert result.response.status_code == 401, result.response.text
    assert result.body.get("code") == 401, result.body


@pytest.mark.admin
@pytest.mark.contract
def test_admin_login_summary_and_settings_contract(admin_client):
    summary = admin_client.get("/admin/api/summary")
    data = assert_api_ok(summary)
    assert data["uptimeSeconds"] >= 0, data
    assert "platformCount" in data, data
    assert "testSampleCount" in data, data
    assert "testLinkCount" in data, data
    assert "node" not in data, data
    assert ("clu" + "ster") not in data, data

    saved = admin_client.post("/admin/api/settings", json={"rateLimitEnabled": True})
    assert_api_ok(saved)


@pytest.mark.admin
@pytest.mark.contract
def test_admin_rejects_bad_login_and_unsupported_profile(client):
    bad_login = client.post(
        "/admin/api/login",
        json={"username": "admin", "password": "definitely-wrong"},
    )
    assert bad_login.response.status_code == 401, bad_login.response.text
    assert_api_error(bad_login, 1001)

    profile = client.get("/api/profile")
    assert profile.response.status_code == 200
    assert_api_error(profile, 1002)
