"""Share-preview and search-indexing helpers shared by publish.py and seo_check.py.

- scan(html)            -> what a page already declares (title, meta, canonical, h1s, ...)
- inject(html, ...)     -> the page with missing preview/robots tags added; author tags always win
- og_image(title, ...)  -> a 1200x630 PNG (needs Pillow; returns None without it)

Parsing uses the stdlib HTML parser, never pattern matching on markup.
"""
from __future__ import annotations

import html as htmllib
import io
import pathlib
from dataclasses import dataclass, field
from html.parser import HTMLParser

NOINDEX = "noindex, nofollow, noarchive"
OG_SIZE = (1200, 630)
OG_PATH = "_ownnow/og.png"  # where publish.py puts the generated image inside the site
FONTS = pathlib.Path(__file__).resolve().parent.parent / "assets" / "fonts"


@dataclass
class Page:
    title: str = ""
    meta: dict = field(default_factory=dict)  # lowercased name/property -> content (first wins)
    canonical: str = ""
    h1: list = field(default_factory=list)
    first_p: str = ""
    imgs_missing_alt: int = 0
    json_ld: int = 0
    head_close: int | None = None  # offset of "</head>"
    head_open_end: int | None = None  # offset just after "<head ...>"
    html_open_end: int | None = None  # offset just after "<html ...>"

    def robots(self) -> str:
        return (self.meta.get("robots", "") + "," + self.meta.get("googlebot", "")).lower()

    def says_noindex(self) -> bool:
        return "noindex" in self.robots() or "none" in [t.strip() for t in self.robots().split(",")]


class _Scanner(HTMLParser):
    def __init__(self, src: str):
        super().__init__(convert_charrefs=True)
        self.page = Page()
        self._line_starts = [0]
        for i, ch in enumerate(src):
            if ch == "\n":
                self._line_starts.append(i + 1)
        self._capture = None  # "title" | "h1" | "p"
        self._buf = []
        self._ld = False

    def _offset(self) -> int:
        line, col = self.getpos()
        return self._line_starts[line - 1] + col

    def handle_starttag(self, tag, attrs):
        a = {k.lower(): (v or "") for k, v in attrs}
        p = self.page
        end = self._offset() + len(self.get_starttag_text() or "")
        if tag == "html" and p.html_open_end is None:
            p.html_open_end = end
        elif tag == "head" and p.head_open_end is None:
            p.head_open_end = end
        elif tag == "meta":
            key = (a.get("property") or a.get("name") or "").lower()
            if key and key not in p.meta:
                p.meta[key] = a.get("content", "")
        elif tag == "link" and "canonical" in a.get("rel", "").lower().split() and not p.canonical:
            p.canonical = a.get("href", "")
        elif tag == "img" and "alt" not in a:  # alt="" is a valid decorative image
            p.imgs_missing_alt += 1
        elif tag == "script" and a.get("type", "").lower() == "application/ld+json":
            p.json_ld += 1
        if tag in ("title", "h1") or (tag == "p" and not p.first_p):
            self._capture, self._buf = tag, []

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)

    def handle_endtag(self, tag):
        p = self.page
        if tag == "head" and p.head_close is None:
            p.head_close = self._offset()
        if tag == self._capture:
            text = " ".join("".join(self._buf).split())
            if tag == "title" and not p.title:
                p.title = text
            elif tag == "h1":
                p.h1.append(text)
            elif tag == "p" and len(text) >= 40:
                p.first_p = text
            self._capture = None

    def handle_data(self, data):
        if self._capture:
            self._buf.append(data)


def scan(src: str) -> Page:
    s = _Scanner(src)
    s.feed(src)
    s.close()
    return s.page


def clip(text: str, limit: int) -> str:
    text = " ".join(text.split())
    if len(text) <= limit:
        return text
    cut = text[: limit - 1].rsplit(" ", 1)[0].rstrip(",.;: ")
    return cut + "…"


def inject(src: str, *, url: str, title: str = "", description: str = "",
           image_url: str = "", image_alt: str = "", indexable: bool | None = None) -> tuple[str, list[str]]:
    """Return (html, added) with missing tags added before </head>.

    Never replaces or edits a tag the author wrote. indexable=False adds a
    robots noindex meta; True and None add none (the server header owns it).
    """
    page = scan(src)
    m = page.meta
    t = page.title or title or (page.h1[0] if page.h1 else "")
    d = m.get("description") or m.get("og:description") or description or page.first_p
    t, d = clip(t, 70), clip(d, 160) if d else ""
    e = lambda v: htmllib.escape(v, quote=True)  # noqa: E731
    tags: list[tuple[str, str]] = []  # (label, markup)
    if not page.title and t:
        tags.append(("title", f"<title>{e(t)}</title>"))
    if "description" not in m and d:
        tags.append(("description", f'<meta name="description" content="{e(d)}">'))
    if not page.canonical:
        tags.append(("canonical", f'<link rel="canonical" href="{e(url)}">'))
    if indexable is False and "robots" not in m:
        tags.append(("robots", f'<meta name="robots" content="{NOINDEX}">'))
    for prop, val in (("og:type", "website"), ("og:url", url), ("og:title", t), ("og:description", d)):
        if prop not in m and val:
            tags.append((prop, f'<meta property="{prop}" content="{e(val)}">'))
    if "og:image" not in m and "twitter:image" not in m and image_url:
        tags.append(("og:image", f'<meta property="og:image" content="{e(image_url)}">'))
        tags.append(("og:image:size", f'<meta property="og:image:width" content="{OG_SIZE[0]}">'
                                      f'<meta property="og:image:height" content="{OG_SIZE[1]}">'))
        if image_alt:
            tags.append(("og:image:alt", f'<meta property="og:image:alt" content="{e(image_alt)}">'))
    if "twitter:card" not in m and ("og:image" in m or "twitter:image" in m or image_url):
        tags.append(("twitter:card", '<meta name="twitter:card" content="summary_large_image">'))
    if not tags:
        return src, []
    block = "\n<!-- ownnow share defaults: added only where the page had none -->\n" + \
            "\n".join(markup for _, markup in tags) + "\n"
    if page.head_close is not None:
        at = page.head_close
    elif page.head_open_end is not None:
        at = page.head_open_end
    elif page.html_open_end is not None:
        at, block = page.html_open_end, "<head>" + block + "</head>"
    else:
        # bare fragment: a leading <head> is still valid and parsed by scrapers
        at, block = 0, "<head>" + block + "</head>\n"
    return src[:at] + block + src[at:], [label for label, _ in tags]


def can_render() -> bool:
    try:
        import PIL  # noqa: F401
        return True
    except ImportError:
        return False


def _font(name: str, size: int, text: str):
    from PIL import ImageFont
    vendored = FONTS / name
    # the vendored fonts are Latin subsets; fall back for anything else
    if vendored.exists() and all(ord(c) < 0x250 or c in "…·" for c in text):
        return ImageFont.truetype(str(vendored), size)
    for fallback in ("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
                     "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
                     "/Library/Fonts/Arial Unicode.ttf"):
        if pathlib.Path(fallback).exists():
            return ImageFont.truetype(fallback, size)
    return ImageFont.load_default(size)


def _wrap(draw, text: str, font, width: int) -> list[str]:
    lines, cur = [], ""
    for word in text.split():
        trial = f"{cur} {word}".strip()
        if draw.textlength(trial, font=font) <= width or not cur:
            cur = trial
        else:
            lines.append(cur)
            cur = word
    if cur:
        lines.append(cur)
    return lines


def og_image(title: str, label: str, footer: str = "") -> bytes | None:
    """Monochrome 1200x630 card: mono label, hairline rules, grotesk title."""
    try:
        from PIL import Image, ImageDraw
    except ImportError:
        return None
    W, H = OG_SIZE
    pad, inner = 72, W - 2 * 72
    bg, fg, dim, line = (10, 10, 10), (242, 242, 240), (138, 138, 134), (52, 52, 50)
    img = Image.new("RGB", OG_SIZE, bg)
    d = ImageDraw.Draw(img)
    d.rectangle([28, 28, W - 29, H - 29], outline=line, width=1)
    mono = _font("geist-mono-latin-400-normal.woff2", 22, label + footer)
    d.text((pad, 76), label.upper(), font=mono, fill=dim)
    d.line([pad, 126, W - pad, 126], fill=line, width=1)
    d.line([pad, H - 126, W - pad, H - 126], fill=line, width=1)
    if footer:
        d.text((pad, H - 100), footer, font=mono, fill=dim)
    title = title.strip() or label
    for size in (84, 72, 60, 52):
        font = _font("inter-tight-latin-500-normal.woff2", size, title)
        lines = _wrap(d, title, font, inner)
        if len(lines) <= 3:
            break
    if len(lines) > 3:
        lines = lines[:3]
        while lines[2] and d.textlength(lines[2] + "…", font=font) > inner:
            lines[2] = lines[2][:-1]
        lines[2] = lines[2].rstrip() + "…"
    lh = int(size * 1.08)
    top = 126 + ((H - 252) - lh * len(lines)) // 2
    for i, ln in enumerate(lines):
        d.text((pad - 2, top + i * lh), ln, font=font, fill=fg)
    out = io.BytesIO()
    img.save(out, "PNG", optimize=True)
    return out.getvalue()


def image_size(data: bytes) -> tuple[int, int] | None:
    try:
        from PIL import Image
        with Image.open(io.BytesIO(data)) as im:
            return im.size
    except ImportError:
        if data[:8] == b"\x89PNG\r\n\x1a\n":
            return int.from_bytes(data[16:20], "big"), int.from_bytes(data[20:24], "big")
    except Exception:
        pass
    return None
