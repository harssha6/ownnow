#!/usr/bin/env python3
"""Publish a file or folder to your ownnow server -> {OWNNOW_SERVER}/{slug}/

Usage:
  publish.py <file-or-dir> [--slug NAME] [--password PW] [--ttl-hours N] [--spa]

Env: OWNNOW_SERVER (your instance), OWNNOW_API_KEY (publish key).
Uploads go presigned straight to object storage when the server uses
S3/R2; otherwise they pass through the server. Either way this script
does not care.
"""
import json, mimetypes, os, pathlib, sys, urllib.request

SERVER = os.environ.get("OWNNOW_SERVER", "http://127.0.0.1:8080").rstrip("/")

def api_key() -> str:
    return os.environ.get("OWNNOW_API_KEY", "")

def call(method, path, key, body=None, data=None, ctype="application/json"):
    req = urllib.request.Request(SERVER + path, data=data if data is not None else (
        json.dumps(body).encode() if body is not None else None), method=method,
        headers={"Authorization": "Bearer " + key, "Content-Type": ctype})
    with urllib.request.urlopen(req, timeout=600) as r:
        return json.load(r) if r.headers.get_content_type() == "application/json" else r.read()

def collect(target: pathlib.Path):
    if target.is_file():
        return [(target.name, target)]
    return [(str(p.relative_to(target)), p) for p in sorted(target.rglob("*")) if p.is_file()]

def main():
    args = sys.argv[1:]
    opts, pos = {}, []
    i = 0
    while i < len(args):
        if args[i] in ("--slug", "--password", "--ttl-hours"):
            opts[args[i][2:]] = args[i + 1]; i += 2
        elif args[i] == "--spa":
            opts["spa"] = True; i += 1
        else:
            pos.append(args[i]); i += 1
    if not pos:
        sys.exit(__doc__)
    target = pathlib.Path(pos[0]).expanduser()
    files = collect(target)
    if not files:
        sys.exit("nothing to publish")
    key = api_key()
    metas = [{"path": p.replace(os.sep, "/"), "size": f.stat().st_size,
              "contentType": mimetypes.guess_type(f.name)[0] or "application/octet-stream"}
             for p, f in files]
    body = {"files": metas, "spa": bool(opts.get("spa"))}
    if opts.get("password"): body["password"] = opts["password"]
    if opts.get("ttl-hours"): body["ttlHours"] = int(opts["ttl-hours"])
    if opts.get("slug"):
        created = call("PUT", f"/api/v1/publish/{opts['slug']}", key, body)
    else:
        created = call("POST", "/api/v1/publish", key, body)
    by_path = {p.replace(os.sep, "/"): f for p, f in files}
    for up in created["uploads"]:
        url = up["url"] if up["url"].startswith("http") else SERVER + up["url"]
        req = urllib.request.Request(url, data=by_path[up["path"]].read_bytes(), method="PUT",
            headers={"Content-Type": mimetypes.guess_type(up["path"])[0] or "application/octet-stream"})
        urllib.request.urlopen(req, timeout=600).read()
        print("  uploaded", up["path"], file=sys.stderr)
    fin = call("POST", f"/api/v1/publish/{created['slug']}/finalize", key,
               {"versionId": created["versionId"]})
    print(fin["siteUrl"])
    if created.get("claimToken"):
        print("claim token (save it — required to update/delete this anonymous site):",
              created["claimToken"], file=sys.stderr)

if __name__ == "__main__":
    main()
