import os
import base64
import time
import uuid
from dataclasses import dataclass
from typing import Any, Dict, Optional
from urllib.parse import urljoin

import pytest
import requests
from Crypto.Cipher import AES


DEFAULT_BASE_URL = "http://localhost:5001"
DEFAULT_MEDIA_URL = "https://www.w3schools.com/html/mov_bbb.mp4"
DEFAULT_SOURCE_URL = "https://v.douyin.com/e2e-test/"
DEFAULT_M3U8_URL = "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8"
DEFAULT_SIGNATURE_KEY = "example-test-key"


@dataclass
class APIResponse:
    response: requests.Response
    body: Dict[str, Any]


class APIClient:
    def __init__(self, base_url: str, timeout: float):
        self.base_url = base_url.rstrip("/") + "/"
        self.timeout = timeout
        self.session = requests.Session()
        self.csrf_token = ""

    def url(self, path: str) -> str:
        return urljoin(self.base_url, path.lstrip("/"))

    def request(self, method: str, path: str, **kwargs: Any) -> APIResponse:
        kwargs.setdefault("timeout", self.timeout)
        headers = dict(kwargs.pop("headers", {}) or {})
        if self.csrf_token and method.upper() in {"POST", "PATCH", "DELETE"} and path.startswith("/admin/api/"):
            headers.setdefault("X-CSRF-Token", self.csrf_token)
            headers.setdefault("Origin", self.base_url.rstrip("/"))
        if headers:
            kwargs["headers"] = headers
        response = self.session.request(method, self.url(path), **kwargs)
        try:
            body = response.json()
        except ValueError:
            body = {}
        return APIResponse(response=response, body=body)

    def get(self, path: str, **kwargs: Any) -> APIResponse:
        return self.request("GET", path, **kwargs)

    def post(self, path: str, **kwargs: Any) -> APIResponse:
        return self.request("POST", path, **kwargs)

    def patch(self, path: str, **kwargs: Any) -> APIResponse:
        return self.request("PATCH", path, **kwargs)

    def delete(self, path: str, **kwargs: Any) -> APIResponse:
        return self.request("DELETE", path, **kwargs)


@pytest.fixture(scope="session")
def base_url() -> str:
    return os.getenv("E2E_BASE_URL", DEFAULT_BASE_URL).rstrip("/")


@pytest.fixture(scope="session")
def api_timeout() -> float:
    return float(os.getenv("E2E_TIMEOUT_SECONDS", "15"))


@pytest.fixture(scope="session")
def download_timeout() -> float:
    return float(os.getenv("E2E_DOWNLOAD_TIMEOUT_SECONDS", "45"))


@pytest.fixture(scope="session")
def client(base_url: str, api_timeout: float) -> APIClient:
    return APIClient(base_url, api_timeout)


@pytest.fixture(scope="session")
def media_url() -> str:
    return os.getenv("E2E_MEDIA_URL", DEFAULT_MEDIA_URL).strip()


@pytest.fixture(scope="session")
def source_url() -> str:
    return os.getenv("E2E_SOURCE_URL", DEFAULT_SOURCE_URL).strip()


@pytest.fixture(scope="session")
def m3u8_url() -> str:
    return os.getenv("E2E_M3U8_URL", DEFAULT_M3U8_URL).strip()


@pytest.fixture(scope="session")
def client_signature_key() -> str:
    return os.getenv("E2E_CLIENT_SIGNATURE_KEY", DEFAULT_SIGNATURE_KEY).strip()


@pytest.fixture(scope="session")
def admin_credentials() -> Dict[str, str]:
    return {
        "username": os.getenv("E2E_ADMIN_USERNAME", "admin"),
        "password": os.getenv("E2E_ADMIN_PASSWORD", "invalid-for-test-only"),
    }


@pytest.fixture()
def admin_client(base_url: str, api_timeout: float, admin_credentials: Dict[str, str]) -> APIClient:
    api = APIClient(base_url, api_timeout)
    login = api.post("/admin/api/login", json=admin_credentials)
    assert login.response.status_code == 200, login.response.text
    assert login.body.get("code") == 0, login.body
    data = login.body.get("data") or {}
    api.csrf_token = data.get("csrfToken", "")
    assert api.csrf_token, data
    return api


@pytest.fixture()
def client_session(client: APIClient) -> Dict[str, Any]:
    client_id = "pytest_" + uuid.uuid4().hex
    payload = {"code": "", "programType": 12, "clientId": client_id}
    result = client.post("/api/client/session", json=payload)
    assert result.response.status_code == 200, result.response.text
    assert result.body.get("code") == 0, result.body
    data = result.body.get("data") or {}
    assert data.get("token"), data
    return data


def assert_http_ok(result: APIResponse) -> None:
    assert 200 <= result.response.status_code < 300, result.response.text


def assert_api_ok(result: APIResponse) -> Dict[str, Any]:
    assert_http_ok(result)
    assert result.body.get("code") == 0, result.body
    data = result.body.get("data")
    return data if isinstance(data, dict) else {"value": data}


def assert_api_error(result: APIResponse, code: Optional[int] = None) -> Dict[str, Any]:
    assert "code" in result.body, result.body
    assert result.body.get("code") != 0, result.body
    if code is not None:
        assert result.body.get("code") == code, result.body
    return result.body


def assert_v1_ok(result: APIResponse) -> Any:
    assert_http_ok(result)
    assert result.body.get("status") == "success", result.body
    return result.body.get("data")


def assert_v1_error(result: APIResponse, status_code: int, error_code: str) -> Dict[str, Any]:
    assert result.response.status_code == status_code, result.response.text
    assert result.body.get("status") == "error", result.body
    error = result.body.get("error") or {}
    assert error.get("code") == error_code, result.body
    return error


def signed_parse_payload(url: str, token: str, signature_key: str, source: int = 12, timestamp: Optional[int] = None) -> Dict[str, Any]:
    timestamp = timestamp or int(time.time())
    plaintext = f"{url}######{timestamp}######{token}######{source}"
    signature = aes_ecb_pkcs7_base64(plaintext, signature_key)
    return {
        "url": url,
        "source": source,
        "timestamp": timestamp,
        "signature": signature,
        "version": 1,
    }


def aes_ecb_pkcs7_base64(plaintext: str, key: str) -> str:
    raw_key = key.encode("utf-8")
    assert len(raw_key) in {16, 24, 32}, "client signature key must be 16/24/32 bytes"
    raw = plaintext.encode("utf-8")
    padding = AES.block_size - (len(raw) % AES.block_size)
    padded = raw + bytes([padding]) * padding
    cipher = AES.new(raw_key, AES.MODE_ECB)
    return base64.b64encode(cipher.encrypt(padded)).decode("ascii")


def wait_for_download_task(
    client: APIClient,
    task_payload: Dict[str, Any],
    timeout_seconds: float = 45,
) -> Dict[str, Any]:
    deadline = time.time() + timeout_seconds
    current = task_payload
    while time.time() < deadline:
        status = current.get("status")
        if status == "completed" or current.get("downloadUrl") or current.get("url"):
            return current
        assert status not in {"failed"}, current
        poll_url = current.get("pollUrl")
        assert poll_url, current
        result = client.get(poll_url)
        data = assert_api_ok(result)
        current = data
        time.sleep(0.5)
    raise AssertionError(f"download fallback task timed out: {current}")
