#!/usr/bin/env python3
"""Generate pkgsite-like HTML snapshots for Go package docs."""

from __future__ import annotations

import atexit
import pathlib
import posixpath
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

MODULE = "github.com/SimonWaldherr/llmflow"
REF_RE = re.compile(r'/static/[^"\')\s>]+')
TEXT_EXTS = {".html", ".htm", ".css", ".js", ".svg", ".xml", ".txt", ".json", ".map", ".ico"}


def fetch(url: str) -> tuple[bytes, str]:
    with urllib.request.urlopen(url) as resp:
        return resp.read(), resp.headers.get_content_type()


def rel_name(pkg: str) -> str:
    rel = pkg[len(MODULE) + 1 :] if pkg.startswith(MODULE + "/") else pkg
    return rel.replace("/", "_").replace(".", "_")


def rewrite_refs(content: str, rel_file: str) -> str:
    rel_dir = posixpath.dirname(rel_file)

    def repl(match: re.Match[str]) -> str:
        raw = match.group(0)
        parsed = urllib.parse.urlsplit(raw)
        target = parsed.path.lstrip("/")
        if not target:
            return raw
        base_dir = rel_dir if rel_dir else "."
        return posixpath.relpath(target, base_dir)

    return REF_RE.sub(repl, content)


def collect_from_text(text: str, refs: set[str]) -> None:
    for ref in REF_RE.findall(text):
        refs.add(ref)


def save_text(path: pathlib.Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def save_binary(path: pathlib.Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)


def start_godoc_server() -> tuple[subprocess.Popen[str], str, pathlib.Path]:
    tmpdir = pathlib.Path(tempfile.mkdtemp(prefix="llmflow-godoc-"))
    logfile = tmpdir / "godoc.log"
    log_handle = logfile.open("w", encoding="utf-8")
    proc = subprocess.Popen(
        ["go", "doc", "-http"],
        stdout=log_handle,
        stderr=subprocess.STDOUT,
        text=True,
    )

    def cleanup() -> None:
        try:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
        finally:
            log_handle.close()
            shutil.rmtree(tmpdir, ignore_errors=True)

    atexit.register(cleanup)

    port = ""
    for _ in range(60):
        if logfile.exists():
            text = logfile.read_text(encoding="utf-8", errors="ignore")
            match = re.search(r"localhost:(\d+)", text)
            if match:
                port = match.group(1)
                break
        time.sleep(1)

    if not port:
        cleanup()
        raise RuntimeError(f"failed to start go doc server; log at {logfile}")

    return proc, port, tmpdir


def fetch_page(base: str, url_path: str, outdir: pathlib.Path, rel_file: str, refs: set[str]) -> None:
    raw, _ = fetch(base + url_path)
    html = raw.decode("utf-8", "replace")
    collect_from_text(html, refs)
    html = html.replace('href="/"', 'href="https://pkg.go.dev/"')
    html = rewrite_refs(html, rel_file)
    save_text(outdir / rel_file, html)


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: generate_godoc.py <output-dir> <module>", file=sys.stderr)
        return 2

    outdir = pathlib.Path(sys.argv[1])
    outdir.mkdir(parents=True, exist_ok=True)
    global MODULE
    MODULE = sys.argv[2]

    packages = subprocess.check_output(["go", "list", "./..."], text=True).split()
    _, port, _ = start_godoc_server()
    base = f"http://127.0.0.1:{port}"

    refs: set[str] = set()
    for pkg in packages:
        fetch_page(base, f"/pkg/{pkg}/", outdir, f"{rel_name(pkg)}.html", refs)

    fetch_page(base, "/", outdir, "index.html", refs)

    pending = set(refs)
    seen: set[str] = set()
    while pending:
        ref = pending.pop()
        if ref in seen:
            continue
        seen.add(ref)

        parsed = urllib.parse.urlsplit(ref)
        asset_path = parsed.path.lstrip("/")
        if not asset_path:
            continue

        asset_url = base + ref
        data, ctype = fetch(asset_url)
        target = outdir / asset_path
        if pathlib.Path(asset_path).suffix.lower() in TEXT_EXTS or ctype.startswith("text/") or ctype in {"application/javascript", "application/json"}:
            text = data.decode("utf-8", "replace")
            collect_from_text(text, refs)
            text = rewrite_refs(text, asset_path)
            save_text(target, text)
        else:
            save_binary(target, data)

        pending.update(refs - seen - pending)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
