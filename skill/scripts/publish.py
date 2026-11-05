#!/usr/bin/env python3
"""Publish a file or folder to your ownnow server -> {OWNNOW_SERVER}/{slug}/

Usage:
  publish.py <file-or-dir> [--slug NAME] [--password PW] [--ttl-hours N] [--spa]
             [--index | --noindex] [--no-share-preview] [--title T] [--description D]

Search: a NEW site is noindex unless you pass --index. Republishing an
existing slug keeps its setting unless you pass --index or --noindex.
Share previews: HTML pages get og:*, twitter:card, canonical and a
generated 1200x630 og image wherever the page has none. Tags you wrote
are never changed. --no-share-preview uploads your files untouched.

Env: OWNNOW_SERVER (your instance), OWNNOW_API_KEY (publish key).
Your files on disk are never modified; injection happens in memory.
"""
import json, mimetypes, os, pathlib, sys, urllib.error, urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import seo  # noqa: E402

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
    return [(str(p.relative_to(target)).replace(os.sep, "/"), p)
            for p in sorted(target.rglob("*")) if p.is_file()]


def page_url(site_url: str, path: str, paths: list) -> str:
    """The URL a page is served at, matching the server's routing."""
    if path == "index.html":
        return site_url
    if path.endswith("/index.html"):
        return site_url + path[: -len("index.html")]
    top_html = [p for p in paths if p.endswith(".html") and "/" not in p]
    if "index.html" not in paths and top_html == [path]:
        return site_url  # single-html fallback serves it at the root
    return site_url + path


def share_pages(paths):
    return [p for p in paths if p.endswith((".html", ".htm")) and p.rsplit("/", 1)[-1] != "404.html"]


def needs_image(data, pages):
    """True when some page has neither og:image nor twitter:image."""
    return any(not ({"og:image", "twitter:image"} & seo.scan(data[p].decode("utf-8", "replace")).meta.keys())
               for p in pages)


def stage(files, site_url, *, slug, indexable, title="", description="", share=True):
    """Return {path: bytes} to upload plus a report. Pure: no network."""
    out = {p: f.read_bytes() for p, f in files}
    report = {}
    if not share:
        return out, report
    pages = share_pages(list(out))
    if not pages:
        return out, report
    root = "index.html" if "index.html" in out else pages[0]
    root_page = seo.scan(out[root].decode("utf-8", "replace"))
    site_title = root_page.title or title or (root_page.h1[0] if root_page.h1 else "") or slug
    image_url = ""
    if needs_image(out, pages) and seo.OG_PATH not in out:
        host = site_url.split("://", 1)[-1].rstrip("/")
        png = seo.og_image(site_title, host)
        if png:
            out[seo.OG_PATH] = png
            image_url = site_url + seo.OG_PATH
        else:
            print("  note: Pillow not installed, no og image generated (pip install pillow)", file=sys.stderr)
    for p in pages:
        src = out[p].decode("utf-8", "replace")
        new, added = seo.inject(src, url=page_url(site_url, p, list(out)), title=title or site_title,
                                description=description, image_url=image_url,
                                image_alt=f"{site_title}, on {site_url.split('://', 1)[-1].rstrip('/')}",
                                indexable=indexable)
        if added:
            out[p] = new.encode("utf-8")
            report[p] = added
        page = seo.scan(src)
        if indexable and page.says_noindex():
            print(f"  warning: {p} has a robots noindex meta but the site is --index; the page wins for that page",
                  file=sys.stderr)
    return out, report


def existing_site(slug, key):
    try:
        return call("GET", f"/api/v1/publish/{slug}", key)["site"]
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise


def main():
    args = sys.argv[1:]
    opts, pos = {}, []
    i = 0
    while i < len(args):
        if args[i] in ("--slug", "--password", "--ttl-hours", "--title", "--description"):
            opts[args[i][2:]] = args[i + 1]; i += 2
        elif args[i] in ("--spa", "--index", "--noindex", "--no-share-preview"):
            opts[args[i][2:]] = True; i += 1
        else:
            pos.append(args[i]); i += 1
    if not pos or (opts.get("index") and opts.get("noindex")):
        sys.exit(__doc__)
    target = pathlib.Path(pos[0]).expanduser()
    files = collect(target)
    if not files:
        sys.exit("nothing to publish")
    key = api_key()

    current = existing_site(opts["slug"], key) if opts.get("slug") else None
    if opts.get("index") or opts.get("noindex"):
        send_indexable = bool(opts.get("index"))
    elif current is None:
        send_indexable = False  # new sites are private to search by default
    else:
        send_indexable = None  # republish: keep whatever the site has
    effective = send_indexable if send_indexable is not None else current.get("indexable")

    paths = [p for p, _ in files]
    share = not opts.get("no-share-preview")
    local = {p: f.read_bytes() for p, f in files if p in share_pages(paths)}
    if share and local and seo.OG_PATH not in paths and seo.can_render() and needs_image(local, list(local)):
        paths.append(seo.OG_PATH)  # reserved now, generated once the URL is known
    sizes = {p: f.stat().st_size for p, f in files}  # pre-injection; the server records the uploaded size
    body = {"files": [{"path": p, "size": sizes.get(p, 0),
                       "contentType": mimetypes.guess_type(p)[0] or "application/octet-stream"} for p in paths],
            "spa": bool(opts.get("spa"))}
    if send_indexable is not None: body["indexable"] = send_indexable
    if opts.get("password"): body["password"] = opts["password"]
    if opts.get("ttl-hours"): body["ttlHours"] = int(opts["ttl-hours"])
    if opts.get("title"): body["displayName"] = opts["title"]
    if opts.get("description"): body["displayDescription"] = opts["description"]
    if opts.get("slug"):
        created = call("PUT", f"/api/v1/publish/{opts['slug']}", key, body)
    else:
        created = call("POST", "/api/v1/publish", key, body)

    data, report = stage(files, created["siteUrl"], slug=created["slug"], indexable=effective,
                         title=opts.get("title", ""), description=opts.get("description", ""), share=share)
    for up in created["uploads"]:
        url = up["url"] if up["url"].startswith("http") else SERVER + up["url"]
        req = urllib.request.Request(url, data=data[up["path"]], method="PUT",
            headers={"Content-Type": mimetypes.guess_type(up["path"])[0] or "application/octet-stream"})
        urllib.request.urlopen(req, timeout=600).read()
        print("  uploaded", up["path"], file=sys.stderr)
    fin = call("POST", f"/api/v1/publish/{created['slug']}/finalize", key,
               {"versionId": created["versionId"]})
    for p, added in report.items():
        print(f"  share preview: {p} +{', '.join(added)}", file=sys.stderr)
    mode = {True: "indexable", False: "noindex", None: "unset (no robots header)"}[effective]
    print(f"  search: {mode}", file=sys.stderr)
    if send_indexable is not None and "indexable" not in (existing_site(created["slug"], key) or {}):
        print("  warning: this server ignores the indexable flag (older ownnow). No X-Robots-Tag is sent;"
              " only the injected robots meta protects HTML pages.", file=sys.stderr)
    print(fin["siteUrl"])
    if created.get("claimToken"):
        print("claim token (save it, required to update/delete this anonymous site):",
              created["claimToken"], file=sys.stderr)


if __name__ == "__main__":
    main()
