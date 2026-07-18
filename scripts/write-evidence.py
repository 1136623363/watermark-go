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
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    tmp_path = pathlib.Path(tmp_name)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, sort_keys=True, indent=2)
            handle.write("\n")
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
    parser = argparse.ArgumentParser(description="Atomically write deployment evidence JSON.")
    parser.add_argument("path", type=pathlib.Path)
    parser.add_argument("--payload", required=True, help="JSON object to write")
    args = parser.parse_args(argv)
    payload = json.loads(args.payload)
    write_evidence(args.path, payload)
    print("PASS")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        raise SystemExit(1)
