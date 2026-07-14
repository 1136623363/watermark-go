import json
import sys
from pathlib import Path


def _load_report(path: Path) -> dict:
    if not path.exists():
        return {}
    return json.loads(path.read_text(encoding="utf-8"))


def _test_status(test: dict) -> str:
    for key in ("outcome", "result"):
        value = test.get(key)
        if value:
            return str(value)
    return "unknown"


def _test_duration(test: dict) -> float:
    value = test.get("duration")
    if isinstance(value, (int, float)):
        return float(value)
    total = 0.0
    for key in ("setup", "call", "teardown"):
        stage = test.get(key)
        if isinstance(stage, dict) and isinstance(stage.get("duration"), (int, float)):
            total += float(stage["duration"])
    return total


def _failure_suggestion(longrepr: str) -> str:
    lower = longrepr.lower()
    if "download" in lower or "fallback" in lower:
        return "重点检查兜底下载模式、源站可访问性、Content-Type、Range 支持、超时配置和节点路由。"
    if "login" in lower or "admin" in lower:
        return "重点检查后台账号密码、Session Cookie、管理员数据和请求超时。"
    if "health" in lower or "mysql" in lower or "redis" in lower:
        return "重点检查 MySQL/Redis 健康状态、连接池、容器网络和启动顺序。"
    if "signature" in lower or "token" in lower:
        return "重点检查客户端 session、签名密钥、时间戳窗口和 token 传递。"
    return "检查接口响应结构、状态码、部署配置和测试输入是否与预期一致。"


def build_recommendations(report: dict) -> str:
    tests = report.get("tests") or []
    summary = report.get("summary") or {}
    failed = [test for test in tests if _test_status(test) in {"failed", "error"}]
    skipped = [test for test in tests if _test_status(test) == "skipped"]
    slow = sorted(tests, key=_test_duration, reverse=True)[:5]

    lines = [
        "# 自动化测试优化建议",
        "",
        "## 总览",
        "",
        f"- 总用例数：{summary.get('total', len(tests))}",
        f"- 通过：{summary.get('passed', 0)}",
        f"- 失败/错误：{summary.get('failed', 0) + summary.get('error', 0) + summary.get('errors', 0)}",
        f"- 跳过：{summary.get('skipped', 0)}",
        "",
        "## 需要优先处理的问题",
        "",
    ]

    if failed:
        for test in failed:
            nodeid = test.get("nodeid", "unknown")
            longrepr = str(test.get("call", {}).get("longrepr") or test.get("longrepr") or "")
            lines.append(f"- `{nodeid}`：{_failure_suggestion(longrepr)}")
    else:
        lines.append("- 当前测试未发现阻断性失败。")
    lines.append("")

    if slow:
        lines.extend(["## 慢用例观察", ""])
        for test in slow:
            duration = _test_duration(test)
            if duration > 0:
                lines.append(f"- `{test.get('nodeid', 'unknown')}`：{duration:.2f}s")
        lines.append("")

    if skipped:
        lines.extend(["## 跳过项", ""])
        for test in skipped[:10]:
            lines.append(f"- `{test.get('nodeid', 'unknown')}`")
        lines.append("")

    lines.extend(
        [
            "## 后续建议",
            "",
            "- 将 `E2E_MEDIA_URL` 固定为一个稳定的小体积 mp4 测试资源，避免公网样本波动影响 CI。",
            "- 生产部署后保留一轮只读健康检查；完整下载链路建议跑在本地或预发环境。",
            "- 持续关注后台任务页中的失败原因、流量、耗时和用户 UID，发现异常模式后补充对应回归用例。",
            "- 如果需要验证工具远程更新状态，手动设置 `E2E_RUN_SLOW=1` 后再运行慢速测试。",
        ]
    )
    return "\n".join(lines) + "\n"


def main() -> int:
    report_path = Path(sys.argv[1] if len(sys.argv) > 1 else "reports/pytest/report.json")
    output_path = Path(sys.argv[2] if len(sys.argv) > 2 else "reports/pytest/recommendations.md")
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(build_recommendations(_load_report(report_path)), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
