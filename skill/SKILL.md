---
name: ownnow
description: >
  Publish files and folders to the web instantly on your own ownnow server.
  Static hosting and privacy-conscious visit stats for HTML sites, images, PDFs,
  and any file type. Use when
  asked to "publish this", "host this", "deploy this", "share this on the
  web", "make a website", "put this online", "upload to the web",
  "create a webpage", "share a link", "serve this site", or
  "generate a URL", "show publish stats", or "how many people opened this". Outputs a live, shareable URL from your ownnow instance.
---

# ownnow

Publish a file/folder → live URL on your own instance. Static hosting only.

## Setup (once)

Point the skill at your server and give it your publish key:

```bash
export OWNNOW_SERVER=https://now.example.com   # your instance
export OWNNOW_API_KEY=...                       # the key from your server's .env
```

Install by copying this `skill/` folder to your agent's skills directory
(e.g. `~/.claude/skills/ownnow`, `~/.cursor/skills/ownnow`), or with the
skills CLI if you published your fork: `npx skills add <you>/ownnow --skill ownnow`.

Or keep your instance settings in a `local/` folder inside the installed
skill, so the rest stays identical to this repo and updates are a plain
copy. The repo ignores `skill/local/`.

- `local/config.json`: `{"server": "https://now.example.com", "keyCommand": ["python3", "local/get_key.py"]}`.
  `keyCommand` runs from the skill folder and prints the key on stdout
  (read it from your secret manager there; never store the key itself).
  The env vars above win when set.
- `local/NOTES.md`: your instance's house rules, auth and ops notes.

**If `local/NOTES.md` exists, read it before publishing. Its rules
override the generic text in this file.**

## Publish

```bash
python3 scripts/publish.py {file-or-dir} [--slug name] [--password pw] [--ttl-hours n] [--spa] \
  [--index | --noindex] [--analytics | --no-analytics] [--no-share-preview] [--title t] [--description d]
```

Prints the live URL (e.g. `https://now.example.com/bright-canvas-a7k2/`).
For HTML sites, `index.html` must be at the root of the folder you publish.
Updates: publish again with the same `--slug` (every publish is a new
version; old versions are kept for rollback). Without an API key the
script still works against servers running in anonymous mode, and prints
a claim token you must save to update/delete that site later.

## Share previews and search (decide on every publish)

Two separate switches. Link previews are always on. Search indexing is a choice.

**Share preview.** `publish.py` gives every HTML page a link preview for
Slack, X, iMessage and the rest: `og:title`, `og:description`, `og:url`,
`og:image` (a generated 1200x630 card at `_ownnow/og.png`), `twitter:card`,
canonical, meta description, and a `<title>` if missing. It only fills
gaps: tags you wrote win, and files on disk are untouched. Write your own
image, title and description when the page deserves better than the
default. `--no-share-preview` uploads the bytes as they are. The image
needs Pillow; without it you get the text tags only.

**Search indexing.** Choose, and name the choice in your reply:

- `--noindex` (the default for a new site): every response for the slug
  carries `X-Robots-Tag: noindex, nofollow, noarchive`, which also covers
  PDFs and images, and HTML pages get a robots meta. Use it for previews,
  drafts, prototypes, client or internal pages, anything with a password,
  and anything the user did not ask to be found.
- `--index`: only when the user wants the page discovered (portfolio,
  blog post, public demo, launch page) or says so. The site joins the
  server's `/sitemap.xml`.
- Unsure: use `--noindex` and tell the user how to flip it.

Republishing a slug keeps its setting unless you pass a flag.

Checklist for an indexable page:

- a unique `<title>` under about 60 chars, a meta description of 120 to 160 chars
- exactly one `<h1>`, alt text on meaningful images
- canonical at the served URL (publish.py adds it) (if the content
  also lives elsewhere, point canonical at the original instead)
- an `og:image` that represents the page
- JSON-LD only for an article, project or person page
- a multi-page site ships its own `sitemap.xml` at the site root (the
  server lists it in `/robots.txt`)

Check: `python3 scripts/seo_check.py <url> [--index|--noindex]` after
publishing, or a folder before. It fails on an indexable page with missing
tags or a noindex signal, a noindex page that can be indexed or sits in a
sitemap, a header that disagrees with the meta tag, and an og image that is
unreachable or the wrong size.

**Flip later.** To hide a site: `PATCH /api/v1/publish/{slug}/metadata`
with `{"indexable": false}`; the header applies at once. To open a site
that was published noindex, republish it with `--index` (same `--slug`):
a PATCH alone leaves the injected robots meta in the HTML. `null` returns a
site to unset. To drop a page that is already in search, keep the URL live
with noindex so crawlers can refetch it (never block it in robots.txt),
then request removal in Google Search Console and Bing Webmaster Tools for
speed. Deleting the site drops it too, more slowly.

noindex is not privacy. Link previews still render, and anyone with the URL
can open the page. Use `--password` for that (a locked page shows no preview).
## Stats and analytics

Built-in server counting is on by default and includes direct PDF, image and
download opens. It stores daily rollups in the instance's existing storage:
counts, daily salted hashes of IP + User-Agent for exact deduplication,
referrer hosts and paths. It stores no raw IPs, raw user agents, cookies,
queries, full referrer URLs, raw events or per-request timestamps. Browser
subresources, HEADs, errors, obvious bots and DNT/GPC requests are excluded.
Multi-range downloads can count more than once. Daily buckets use UTC; period
uniques sum daily uniques. Rollups remain until site deletion, manual removal
or an operator's object-store lifecycle rule.

Read stats without publishing or changing the site:

```bash
python3 scripts/stats.py <slug> --days 7
# Or, with the Go binary installed:
ownnow stats <slug> --days 7
```

Both return JSON with `totals` (`views`, `uniqueVisitors`, `fileOpens`), `series`,
`topReferrers` (`host`, `views`) and `topPaths` (`path`, `views`). File opens are
a subset of views. `days` defaults to 7 and accepts 1–365. Missing dates are
zero-filled. Stats use the publishing key or `OWNNOW_CLAIM`, even in local-dev
mode. Keys/claims stay in environment or the local overlay, never in page HTML.

Optional server-injected Umami/Plausible/custom trackers are off by default.
An operator can enable them through `OWNNOW_CONFIG`; existing tracker tags skip
injection. `publish.py --no-analytics` opts this publish out; `--analytics`
re-enables injection. Omitted flags preserve the setting. These flags **leave
built-in counting on** and do not remove trackers already in uploaded HTML.
Only the operator's `OWNNOW_COUNTING=false` stops new server counts, keeping
history readable. Unset `OWNNOW_CONFIG` or remove its `analytics` block to stop
instance-wide injection. Do not add or remove instance trackers without an
explicit user request.

The optional Cloudflare Worker needs its stats bridge configured before its
traffic appears here. Full setup and API contract: `docs/stats-and-analytics.md`
in the repository. The runnable HTML/PDF walkthrough is `examples/stats-demo/`.

## Manage

With `OWNNOW_API_KEY` set, against `$OWNNOW_SERVER/api/v1`:

- `GET /publishes`: list sites
- `GET /publishes/{slug}/stats?days=7`: authenticated stats (read-only)
- `GET /publish/{slug}` / `GET /publish/{slug}/versions`: detail, history
- `POST /publish/{slug}/rollback {"versionId":"v_..."}`: roll back
- `PATCH /publish/{slug}/metadata`: name, password, TTL, SPA mode, `indexable` (true, false, or null to unset)
- `DELETE /publish/{slug}`: delete
