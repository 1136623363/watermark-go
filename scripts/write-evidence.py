#!/usr/bin/env python3
"""Atomic writer for machine-readable deployment evidence."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import stat
import sys
import tempfile
from typing import Any


def write_evidence(path: str | pathlib.Path, payload: dict[str, Any]) -> None:
    validate_payload(payload)
    text = json.dumps(payload, ensure_ascii=False, sort_keys=True, indent=2) + "\n"
    write_text_atomic(path, text)


def write_markdown_evidence(path: str | pathlib.Path, payload: dict[str, Any], summary: str) -> None:
    validate_payload(payload)
    lines = ["---"]
    for key, value in ordered_payload_items(payload):
        lines.append(f"{key}: {yaml_scalar(value)}")
    lines.extend(["---", "", summary.strip() or "Verification evidence recorded.", ""])
    write_text_atomic(path, "\n".join(lines))


def write_text_atomic(path: str | pathlib.Path, text: str) -> None:
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    tmp_path = pathlib.Path(tmp_name)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_path, path)
        chmod_0600(path)
        fsync_directory(path.parent)
    except Exception:
        try:
            tmp_path.unlink(missing_ok=True)
        finally:
            raise


def validate_payload(payload: dict[str, Any]) -> None:
    if not isinstance(payload, dict):
        raise ValueError("evidence payload must be a JSON object")
    if not isinstance(payload.get("schemaVersion"), int):
        raise ValueError("evidence payload requires integer schemaVersion")
    if not isinstance(payload.get("passed"), bool):
        raise ValueError("evidence payload requires boolean passed")
    if payload.get("passed") is True and not (payload.get("deploymentRunId") or payload.get("runId") or payload.get("role")):
        raise ValueError("passed evidence requires deploymentRunId, runId, or role")


def ordered_payload_items(payload: dict[str, Any]) -> list[tuple[str, Any]]:
    preferred = ["schemaVersion", "passed", "runId", "deploymentRunId", "cutoverAttemptId", "role", "sourceCommit"]
    items: list[tuple[str, Any]] = []
    seen: set[str] = set()
    for key in preferred:
        if key in payload:
            items.append((key, payload[key]))
            seen.add(key)
    for key in sorted(payload):
        if key not in seen:
            items.append((key, payload[key]))
    return items


def yaml_scalar(value: Any) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if value is None:
        return "null"
    text = str(value)
    if re_match_safe_yaml_plain(text):
        return text
    return json.dumps(text, ensure_ascii=False)


def re_match_safe_yaml_plain(text: str) -> bool:
    import re

    return bool(re.fullmatch(r"[A-Za-z0-9_.@/-]+", text))


def chmod_0600(path: pathlib.Path) -> None:
    current = stat.S_IMODE(path.stat().st_mode)
    if current != 0o600:
        path.chmod(0o600)


def fsync_directory(path: pathlib.Path) -> None:
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Atomically write deployment evidence.")
    parser.add_argument("path", nargs="?", type=pathlib.Path, help="Evidence path for legacy --payload mode")
    parser.add_argument("--payload", help="JSON object to write")
    parser.add_argument("--output", type=pathlib.Path, help="Evidence path")
    parser.add_argument("--format", choices=("json", "markdown"), default="json")
    parser.add_argument("--schema-version", type=int)
    parser.add_argument("--passed", choices=("true", "false"))
    parser.add_argument("--run-id")
    parser.add_argument("--deployment-run-id")
    parser.add_argument("--cutover-attempt-id")
    parser.add_argument("--role")
    parser.add_argument("--source-commit")
    parser.add_argument("--field", action="append", default=[], help="Additional evidence field as key=value")
    parser.add_argument("--summary", default="", help="Markdown body summary for --format markdown")
    args = parser.parse_args(argv)
    output = args.output or args.path
    if output is None:
        raise ValueError("evidence output path is required")
    if args.payload:
        payload = json.loads(args.payload)
    else:
        payload = build_payload_from_args(args)
    if args.format == "markdown":
        write_markdown_evidence(output, payload, args.summary)
    else:
        write_evidence(output, payload)
    print("PASS")
    return 0


def build_payload_from_args(args: argparse.Namespace) -> dict[str, Any]:
    if args.schema_version is None:
        raise ValueError("--schema-version is required without --payload")
    if args.passed is None:
        raise ValueError("--passed is required without --payload")
    payload: dict[str, Any] = {
        "schemaVersion": args.schema_version,
        "passed": args.passed == "true",
    }
    optional_fields = {
        "runId": args.run_id,
        "deploymentRunId": args.deployment_run_id,
        "cutoverAttemptId": args.cutover_attempt_id,
        "role": args.role,
        "sourceCommit": args.source_commit,
    }
    for key, value in optional_fields.items():
        if value is not None:
            payload[key] = value
    for field in args.field:
        key, separator, value = field.partition("=")
        if separator != "=" or not key:
            raise ValueError("--field must be key=value")
        if key in payload:
            raise ValueError(f"duplicate evidence field {key}")
        payload[key] = value
    return payload


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        raise SystemExit(1)
