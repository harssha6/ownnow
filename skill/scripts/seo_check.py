#!/usr/bin/env python3
"""Lint a page or site for share previews and search indexing.

Usage:
  seo_check.py <dir-or-url> [--index | --noindex]

Mode: --index checks a page meant to be found; --noindex checks a page
that must stay out of search. Without a flag, a URL is judged by its
X-Robots-Tag header and a folder by its robots meta (none = noindex,
the publish default).

URL mode checks what crawlers and link unfurlers actually get: headers,
tags, the og image (reachable, 1200x630) and the sitemap. Folder mode
checks what you wrote before publishing; tags publish.py would add are
warnings there, not errors.

Exit status: 0 clean or warnings only, 1 errors, 2 bad usage.
"""
import pathlib, sys, urllib.error, urllib.parse, urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import seo  # noqa: E402

UA = "ownnow-seo-check/1 (+https://github.com/harssha6/ownnow)"


class Report:
    def __init__(self):
        self.errors = self.warnings = 0

    def err(self, where, msg):
        self.errors += 1
        print(f"  ✗ {where}: {msg}")

    def warn(self, where, msg):
        self.warnings += 1
        print(f"  ! {where}: {msg}")

    def ok(self, where, msg):
        print(f"  ✓ {where}: {msg}")


def fetch(url, timeout=15):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status, r.headers, r.read()


def check_image(rep, where, image_url, severe):
    complain = rep.err if severe else rep.warn
    if not urllib.parse.urlsplit(image_url).scheme:
        complain(where, f"og:image must be an absolute URL, got {image_url!r}")
        return
    try:
        status, headers, data = fetch(image_url)
    except (urllib.error.URLError, OSError) as e:
        complain(where, f"og:image unreachable: {image_url} ({e})")
        return
    size = seo.image_size(data)
    if size is None:
        complain(where, f"og:image is not a readable image ({headers.get('Content-Type')})")
    elif size != seo.OG_SIZE:
        rep.warn(where, f"og:image is {size[0]}x{size[1]}; 1200x630 renders best everywhere")
    else:
        rep.ok(where, "og:image reachable, 1200x630")


def check_page(rep, where, page, indexable, *, published, header_noindex=None):
    """Shared tag checks. published=False softens what publish.py injects."""
    soft = rep.warn if not published else rep.err
    injected = "" if published else " (publish.py adds a default)"
    m = page.meta
    meta_noindex = page.says_noindex()

    # share preview: wanted in both modes
    for tag in ("og:title", "og:image"):
        if tag not in m and not (tag == "og:image" and "twitter:image" in m):
            (soft if indexable else rep.warn)(where, f"missing {tag}{injected}")
    if "twitter:card" not in m:
        rep.warn(where, f"missing twitter:card{injected}")

    if indexable:
        if not page.title:
            soft(where, f"missing <title>{injected}")
        elif len(page.title) > 60:
            rep.warn(where, f"title is {len(page.title)} chars; keep it under about 60")
        d = m.get("description", "")
        if not d:
            soft(where, f"missing meta description{injected}")
        elif not 120 <= len(d) <= 160:
            rep.warn(where, f"description is {len(d)} chars; aim for 120 to 160")
        if not page.canonical:
            soft(where, f"missing canonical{injected}")
        if len(page.h1) != 1:
            rep.warn(where, f"{len(page.h1)} <h1> elements; use exactly one")
        if page.imgs_missing_alt:
            rep.warn(where, f"{page.imgs_missing_alt} <img> without alt")
        if meta_noindex:
            rep.err(where, f"meta robots says {page.robots().strip(',')!r} on a page meant to be indexed")
        if header_noindex:
            rep.err(where, "X-Robots-Tag noindex on a page meant to be indexed")
    else:
        if published and header_noindex is False and not meta_noindex:
            rep.err(where, "no X-Robots-Tag and no robots meta: this page can be indexed")
        elif published and header_noindex is False:
            rep.warn(where, "noindex only in the meta tag; PDFs and images on this site are not covered")
        elif not published and not meta_noindex:
            rep.warn(where, "no robots meta; fine on ownnow (the header covers it), add one elsewhere")
        if header_noindex and m.get("robots") and not meta_noindex:
            rep.err(where, f"header says noindex but meta robots says {m['robots']!r}")
        if page.json_ld:
            rep.warn(where, "JSON-LD on a noindex page does nothing; drop it or index the page")


def in_sitemap(page_url):
    """Look for the URL in the origin sitemap and the site's own sitemap."""
    parts = urllib.parse.urlsplit(page_url)
    origin = f"{parts.scheme}://{parts.netloc}"
    first = parts.path.strip("/").split("/")[0]
    candidates = [origin + "/sitemap.xml"]
    if first:
        candidates.append(f"{origin}/{first}/sitemap.xml")
    for sm in candidates:
        try:
            _, _, body = fetch(sm)
        except (urllib.error.URLError, OSError):
            continue
        if f"<loc>{page_url}</loc>".encode() in body or f"<loc>{page_url.rstrip('/')}/</loc>".encode() in body:
            return sm
    return None


def check_url(url, mode):
    rep = Report()
    try:
        status, headers, body = fetch(url)
    except urllib.error.HTTPError as e:
        status, headers, body = e.code, e.headers, e.read()
    xrt = (headers.get("X-Robots-Tag") or "").lower()
    header_noindex = "noindex" in xrt
    indexable = (not header_noindex) if mode is None else mode
    print(f"{url}  [{status}]  mode: {'index' if indexable else 'noindex'}"
          f"{' (inferred from header)' if mode is None else ''}")
    if status != 200:
        rep.err("page", f"HTTP {status}; crawlers and unfurlers see nothing useful")
    ctype = headers.get("Content-Type", "")
    if "html" in ctype:
        page = seo.scan(body.decode("utf-8", "replace"))
        check_page(rep, "page", page, indexable, published=True, header_noindex=header_noindex)
        if "og:image" in page.meta or "twitter:image" in page.meta:
            check_image(rep, "page", page.meta.get("og:image") or page.meta["twitter:image"], severe=indexable)
        if page.canonical and indexable and page.canonical.rstrip("/") != url.rstrip("/"):
            rep.warn("page", f"canonical points elsewhere: {page.canonical}")
    elif not indexable and not header_noindex:
        rep.err("file", f"{ctype or 'non-HTML'} file without X-Robots-Tag noindex can be indexed")
    listed = in_sitemap(url)
    if indexable and not listed:
        rep.warn("sitemap", "not listed in the origin sitemap.xml or the site's own sitemap.xml")
    elif indexable:
        rep.ok("sitemap", f"listed in {listed}")
    elif listed:
        rep.err("sitemap", f"a noindex page is listed in {listed}")
    if indexable and "noindex" not in xrt:
        rep.ok("robots", "no noindex header")
    elif not indexable and header_noindex:
        rep.ok("robots", f"X-Robots-Tag: {headers.get('X-Robots-Tag')}")
    return rep


def check_dir(root, mode):
    rep = Report()
    pages = [p for p in sorted(root.rglob("*")) if p.suffix in (".html", ".htm") and p.name != "404.html"]
    if not pages:
        print(f"{root}: no HTML pages; only the server header controls indexing for these files")
        return rep
    for p in pages:
        page = seo.scan(p.read_text("utf-8", "replace"))
        indexable = (not page.says_noindex()) if mode is None and "robots" in page.meta else bool(mode)
        rel = str(p.relative_to(root))
        print(f"{rel}  mode: {'index' if indexable else 'noindex'}")
        check_page(rep, rel, page, indexable, published=False)
        img = page.meta.get("og:image") or page.meta.get("twitter:image")
        if img:
            check_image(rep, rel, img, severe=False)
    if mode and not (root / "sitemap.xml").exists():
        rep.warn("sitemap", "no sitemap.xml; fine for one page (the ownnow root sitemap lists the site), "
                            "add one for multi-page sites")
    return rep


def main(argv):
    flags = {a for a in argv if a.startswith("--")}
    pos = [a for a in argv if not a.startswith("--")]
    if len(pos) != 1 or flags - {"--index", "--noindex"} or flags == {"--index", "--noindex"}:
        print(__doc__)
        return 2
    mode = True if "--index" in flags else False if "--noindex" in flags else None
    target = pos[0]
    if target.startswith(("http://", "https://")):
        rep = check_url(target, mode)
    else:
        rep = check_dir(pathlib.Path(target).expanduser(), mode)
    print(f"{rep.errors} error(s), {rep.warnings} warning(s)")
    return 1 if rep.errors else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
