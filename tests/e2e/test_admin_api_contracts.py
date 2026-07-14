import os
import time

import pytest

from conftest import assert_api_error, assert_api_ok


ADMIN_READ_ENDPOINTS = [
    "/admin/api/summary",
    "/admin/api/parse/attempts",
    "/admin/api/test/platform-runs/latest",
    "/admin/api/test/samples",
    "/admin/api/wechat-domains?status=all&limit=5",
    "/admin/api/cache?limit=5",
    "/admin/api/tasks",
    "/admin/api/download-fallback?hours=24&limit=5",
    "/admin/api/requests?limit=5",
    "/admin/api/logs?file=app&lines=5",
    "/admin/api/diagnostics",
    "/admin/api/tools",
    "/api/settings",
]


@pytest.mark.admin
@pytest.mark.contract
@pytest.mark.parametrize(
    ("method", "path", "kwargs"),
    [
        ("GET", "/admin/api/summary", {}),
        ("POST", "/admin/api/parse", {"json": {"url": "https://example.com"}}),
        ("GET", "/api/settings", {}),
        ("POST", "/api/settings", {"json": {}}),
    ],
)
def test_admin_endpoints_require_session(client, method, path, kwargs):
    result = client.request(method, path, **kwargs)
    assert result.response.status_code == 401, result.response.text
    assert result.body.get("code") == 401, result.body


@pytest.mark.admin
@pytest.mark.contract
def test_admin_page_and_logout_flow(admin_client):
    page = admin_client.get("/admin")
    assert page.response.status_code == 200, page.response.text[:300]
    assert "text/html" in page.response.headers.get("content-type", "")

    login_page = admin_client.get("/admin/login", allow_redirects=False)
    assert login_page.response.status_code == 302
    assert login_page.response.headers.get("Location") == "/admin"

    logout = admin_client.post("/admin/logout")
    assert_api_ok(logout)

    after_logout = admin_client.get("/admin/api/summary")
    assert after_logout.response.status_code == 401
    assert after_logout.body.get("code") == 401, after_logout.body


@pytest.mark.admin
@pytest.mark.contract
@pytest.mark.parametrize("path", ADMIN_READ_ENDPOINTS)
def test_admin_read_endpoints_return_stable_contract(admin_client, path):
    result = admin_client.get(path)
    data = assert_api_ok(result)
    assert data is not None


@pytest.mark.admin
@pytest.mark.contract
def test_admin_error_contracts(admin_client):
    invalid_parse = admin_client.post(
        "/admin/api/parse",
        data="not-json",
        headers={"Content-Type": "application/json"},
    )
    assert invalid_parse.response.status_code == 400
    assert_api_error(invalid_parse, 1004)

    bad_log = admin_client.get("/admin/api/logs?file=unknown")
    assert bad_log.response.status_code == 400
    assert_api_error(bad_log, 1004)

    missing_cache = admin_client.get("/admin/api/cache/not-a-real-cache-id")
    assert missing_cache.response.status_code == 404
    assert_api_error(missing_cache, 1004)

    delete_missing_cache = admin_client.delete("/admin/api/cache/not-a-real-cache-id")
    deleted = assert_api_ok(delete_missing_cache)
    assert deleted["deleted"] is False

    missing_run = admin_client.get("/admin/api/test/platform-runs/not-a-real-run")
    assert missing_run.response.status_code == 404
    assert_api_error(missing_run, 1004)

    invalid_samples = admin_client.post(
        "/admin/api/test/samples",
        data="not-json",
        headers={"Content-Type": "application/json"},
    )
    assert invalid_samples.response.status_code == 400
    assert_api_error(invalid_samples, 1004)

    bad_domain_id = admin_client.patch("/admin/api/wechat-domains/not-int", json={"status": "confirmed"})
    assert bad_domain_id.response.status_code == 400
    assert_api_error(bad_domain_id, 1004)

    bad_domain_status = admin_client.patch("/admin/api/wechat-domains/999999999", json={"status": "bad-status"})
    assert bad_domain_status.response.status_code == 400
    assert_api_error(bad_domain_status, 1004)

    bad_tool = admin_client.post("/admin/api/tools/no-such-component/update")
    assert bad_tool.response.status_code == 400
    assert_api_error(bad_tool, 1004)

    invalid_settings = admin_client.post(
        "/api/settings",
        data="not-json",
        headers={"Content-Type": "application/json"},
    )
    assert invalid_settings.response.status_code == 400
    assert_api_error(invalid_settings, 1004)


@pytest.mark.admin
@pytest.mark.contract
def test_admin_wechat_domain_export_contract(admin_client):
    result = admin_client.post("/admin/api/wechat-domains/export")
    data = assert_api_ok(result)
    assert "domains" in data or "content" in data or "path" in data, data


@pytest.mark.admin
@pytest.mark.e2e
def test_admin_platform_test_inline_sample_flow(admin_client, m3u8_url):
    link = {"platform": "m3u8", "name": "pytest m3u8", "url": m3u8_url, "enabled": True}

    sync_run = admin_client.post("/admin/api/test/platforms", json={"links": [link]})
    sync_data = assert_api_ok(sync_run)
    assert sync_data["total"] == 1, sync_data
    assert len(sync_data["items"]) == 1, sync_data

    started = admin_client.post("/admin/api/test/platform-runs", json={"links": [link]})
    start_data = assert_api_ok(started)
    run = start_data["run"]
    run_id = run["runId"]
    assert run["total"] == 1, run

    deadline = time.time() + 20
    snapshot = run
    while time.time() < deadline:
        result = admin_client.get(f"/admin/api/test/platform-runs/{run_id}")
        snapshot = assert_api_ok(result)["run"]
        if snapshot.get("completed", 0) >= snapshot.get("total", 1):
            break
        time.sleep(0.5)

    assert snapshot.get("completed") == snapshot.get("total") == 1, snapshot
    assert snapshot.get("durationMs", 0) >= 0, snapshot
    assert snapshot.get("items"), snapshot


@pytest.mark.admin
@pytest.mark.slow
def test_admin_tool_check_contract_optional(admin_client):
    if os.getenv("E2E_RUN_SLOW") != "1":
        pytest.skip("tool remote checks are optional because they depend on network and upstream services")

    result = admin_client.post("/admin/api/tools/check")
    data = assert_api_ok(result)
    assert "items" in data, data
