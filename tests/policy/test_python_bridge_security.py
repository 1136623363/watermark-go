import ast
import importlib.util
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
BRIDGE = ROOT / "bridges" / "universal" / "python" / "bridge.py"


def parse_bridge():
    return ast.parse(BRIDGE.read_text(encoding="utf-8"), filename=str(BRIDGE))


def load_bridge_module():
    spec = importlib.util.spec_from_file_location("watermark_python_bridge_policy", BRIDGE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_python_bridge_removes_callers_request_override_escape_hatches():
    tree = parse_bridge()
    forbidden_names = {"requestOverride", "requests_overrides", "request_override"}
    hits = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and node.value in forbidden_names:
            hits.append((node.lineno, node.value))
        if isinstance(node, ast.Name) and node.id in forbidden_names:
            hits.append((node.lineno, node.id))
        if isinstance(node, ast.keyword) and node.arg in forbidden_names:
            hits.append((node.lineno, node.arg))
    assert not hits, f"bridge still exposes request override hooks: {hits}"


def test_python_bridge_parses_guard_proxy_and_clamps_network_options():
    tree = parse_bridge()
    guard_proxy_reads = []
    proxy_environment_names = set()
    unsafe_keywords = {"proxies", "verify", "stream", "allow_redirects", "headers", "cookies"}
    unsafe_hits = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and node.value == "--guard-proxy":
            guard_proxy_reads.append(node.lineno)
        if isinstance(node, ast.Constant) and isinstance(node.value, str) and node.value.endswith("_PROXY") or (
            isinstance(node, ast.Constant) and isinstance(node.value, str) and node.value.endswith("_proxy")
        ):
            proxy_environment_names.add(node.value)
        if isinstance(node, ast.keyword) and node.arg in unsafe_keywords:
            unsafe_hits.append((node.lineno, node.arg))
    assert guard_proxy_reads, "bridge.py must parse the --guard-proxy argv supplied by Go"
    assert {"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}.issubset(proxy_environment_names)
    assert not unsafe_hits, f"bridge forwards caller-controlled network options: {unsafe_hits}"


def test_python_bridge_has_no_raw_socket_requests_or_subprocess_egress():
    tree = parse_bridge()
    forbidden_imports = {"requests", "socket", "subprocess", "urllib.request", "http.client"}
    hits = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                if alias.name in forbidden_imports:
                    hits.append((node.lineno, alias.name))
        if isinstance(node, ast.ImportFrom) and node.module in forbidden_imports:
            hits.append((node.lineno, node.module))
    assert not hits, f"bridge imports raw egress primitives outside sandbox adapter: {hits}"


def test_python_bridge_rejects_non_loopback_guard_proxy_endpoints():
    bridge = load_bridge_module()
    assert bridge.parse_cli_args(["bridge.py", "video", "--guard-proxy", "http://127.0.0.1:18080/"]) == (
        "video",
        "http://127.0.0.1:18080",
    )
    for endpoint in (
        "https://127.0.0.1:18080",
        "http://localhost:18080",
        "http://192.168.1.8:18080",
        "http://user:pass@127.0.0.1:18080",
        "http://127.0.0.1",
        "http://127.0.0.1:18080/path",
        "http://127.0.0.1:18080?target=http://evil.example",
        "http://127.0.0.1:18080#fragment",
    ):
        try:
            bridge.parse_cli_args(["bridge.py", "video", "--guard-proxy", endpoint])
        except ValueError:
            continue
        raise AssertionError(f"accepted unsafe guard proxy endpoint {endpoint!r}")


def test_python_bridge_rejects_musicdl_network_override_config():
    bridge = load_bridge_module()
    safe = bridge.read_musicdl_config({"musicConfigJson": '{"sources":{"TIDALMusicClient":{"quality":"lossless"}}}'})
    assert safe == {"sources": {"TIDALMusicClient": {"quality": "lossless"}}}
    for raw in (
        '{"sources":{"TIDALMusicClient":{"requests_overrides":{"headers":{"Cookie":"x"}}}}}',
        '{"sources":{"TIDALMusicClient":{"proxies":{"https":"http://evil.example:8080"}}}}',
        '{"sources":{"TIDALMusicClient":{"verify":false}}}',
        '{"sources":{"TIDALMusicClient":{"stream":true}}}',
        '{"sources":{"TIDALMusicClient":{"allow_redirects":true}}}',
        '{"sources":{"TIDALMusicClient":{"session":{"id":"x"}}}}',
    ):
        try:
            bridge.read_musicdl_config({"musicConfigJson": raw})
        except ValueError:
            continue
        raise AssertionError(f"accepted unsafe musicdl config {raw!r}")
