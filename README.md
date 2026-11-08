<p align="center">
  <a href="https://ownnow.dev"><img src="examples/demo-site/og.png" alt="ownnow: Your agent ships it. You own the link." width="720"></a>
</p>

# ownnow

**Your agent ships it. You own the link.**

Tell your agent "publish this" and get a live URL in seconds. Your
server, your domain, your storage. ownnow is an open-source here.now
alternative: one Go binary, MIT licensed, nobody in the middle.

```bash
ownnow publish ./my-site
# -> https://now.yourdomain.com/bright-canvas-a7k2/
```

![ownnow demo: set up with Docker, tell an agent to publish, open the live URL](assets/demo-v5.gif)

**Website:** [ownnow.dev](https://ownnow.dev) ·
**Install:** grab the binary for macOS, Linux, or Windows from the
[latest release](https://github.com/harssha6/ownnow/releases), or build
from source below.

![license](https://img.shields.io/badge/license-MIT-green) Static sites,
images, PDFs, Wasm apps, any file type.

## How it works

Three calls, and a site is never half-live:

1. **Create.** The server reserves a slug like `bright-canvas-a7k2` and
   returns signed upload URLs.
2. **Upload.** Files go straight to your storage over presigned URLs.
   They never pass through the server.
3. **Finalize.** One atomic switch publishes the version. Every version
   is kept, so rollback is a pointer flip.

## Bring your own infra

| Path | You need | Serving cost |
|---|---|---|
| **Docker anywhere** (default) | Any VPS and a domain | Your host's bandwidth |
| **Cloudflare** | A domain on Cloudflare and an R2 bucket | **$0 egress** on R2, Workers free tier |
| **Single binary** | Nothing | Local development only |

### 1. Docker anywhere

```bash
git clone https://github.com/harssha6/ownnow && cd ownnow
cp .env.example .env        # set OWNNOW_DOMAIN, OWNNOW_API_KEY, OWNNOW_SECRET
docker compose up -d
```

Point your domain's DNS `A` record at the host. Caddy provisions TLS
automatically. Sites live at `https://<your-domain>/{slug}/`, or at
`https://{slug}.<your-domain>/` if you add wildcard DNS and set
`OWNNOW_BASE_DOMAIN`. Storage is a local volume by default; set
`OWNNOW_STORAGE=s3` to use R2, S3, or MinIO instead.

### 2. Cloudflare (zero-egress serving)

The Go binary becomes a publish-only control plane that can run
anywhere, even a laptop. A TypeScript Worker serves traffic from R2 at
the edge:

```bash
cd worker && bun install
bunx wrangler r2 bucket create ownnow
cp wrangler.example.toml wrangler.toml   # your domain + bucket
bun run deploy                            # wildcard {slug}.your-domain
```

Run the Go server with `OWNNOW_STORAGE=s3` and your R2 keys (see
`.env.example`). Uploads are presigned, so file bytes never pass
through the server.

### 3. Single binary

```bash
go build -o ownnow ./cmd/ownnow
OWNNOW_API_KEY=$(openssl rand -hex 32) ./ownnow serve   # :8080, local disk
```

## Publishing

```bash
ownnow publish ./site [--slug name] [--password pw] [--ttl-hours 24] [--spa] [--index|--noindex] [--no-analytics]
ownnow list
ownnow stats <slug> [--days 7]
ownnow rollback <slug> <version-id>
ownnow delete <slug>
```

**Included:** version history and rollback, password-protected sites,
TTL and expiry, anonymous 24-hour sites with claim tokens, SPA
fallback, custom `404.html`, directory listings, correct
`application/wasm` plus COOP and COEP headers for Wasm and games, and
Range requests.

**Search and link previews:** each site has an `indexable` switch.
`false` sends `X-Robots-Tag: noindex, nofollow, noarchive` on every
response for the site, PDFs and images included. `true` lists the site in
the server's `/sitemap.xml`. Unset serves the site exactly as before. The
root `/robots.txt` allows all crawling on purpose, so crawlers can see the
noindex header. The agent skill's `publish.py` makes new sites noindex
unless told `--index`, and adds Open Graph tags, a canonical link and a
generated 1200x630 preview image wherever a page has none.
`skill/scripts/seo_check.py` lints a page for either mode.

**For AI agents:** an agent skill lives in [`skill/`](skill/). Point
Claude, Cursor, or another agent at it, set `OWNNOW_SERVER` and
`OWNNOW_API_KEY` (or a gitignored `skill/local/config.json`, see the
skill's Setup), and "publish this" works against your instance.

## Analytics

Built-in counting is **on by default** for sites served by the Go server, with
local disk or S3/R2 storage. It counts successful GETs (200, 206 and 304),
including PDF, image and download opens. `views` includes both HTML views and
file opens; `fileOpens` is the non-HTML subset. Browser subresource requests
(images embedded in a page, CSS, JS, fonts) are excluded using `Sec-Fetch-Dest`.
Direct file navigation is counted. A download using several Range requests
counts each request; these are opens/requests, not completed downloads.

HEADs, errors, password prompts, expired sites, obvious bot user agents, empty
user agents and requests without `Accept` are excluded. `DNT: 1` and
`Sec-GPC: 1` skip counting and optional tracker injection. Existing scripts in
published files still follow their publisher's own privacy behavior. Bot
filtering is deliberately small and can be evaded; counts are not billing data.

```bash
OWNNOW_SERVER=https://now.example.com OWNNOW_API_KEY=... ownnow stats my-site --days 7
curl -H "Authorization: Bearer $OWNNOW_API_KEY" \
  'https://now.example.com/api/v1/publishes/my-site/stats?days=7'
```

The CLI prints the same JSON as the API. `days` defaults to 7, accepts 1–365,
and includes today. Daily buckets use **UTC**, oldest first, with zero-filled
missing dates. `uniqueVisitors` deduplicates a daily salted HMAC-SHA256 of
IP + User-Agent, scoped to the publish. Period totals **sum daily uniques**;
the same person on two days counts twice. No cross-day identity is retained.

```json
{
  "slug": "my-site",
  "days": 1,
  "timezone": "UTC",
  "countingEnabled": true,
  "totals": {"views": 3, "uniqueVisitors": 1, "fileOpens": 1},
  "series": [{"date": "2026-11-09", "views": 3, "uniqueVisitors": 1, "fileOpens": 1}],
  "topReferrers": [{"host": "example.com", "views": 2}],
  "topPaths": [{"path": "/", "views": 2}, {"path": "/report.pdf", "views": 1}]
}
```

Stats require the publishing bearer key, or the site's claim token through
`X-OwnNow-Claim` / `Authorization: Bearer`. This also applies in local-dev mode;
there is no public stats endpoint. Invalid/missing credentials yield 401/403,
a missing site 404, and invalid `days` 400. Responses have `Cache-Control:
no-store`. Top lists return up to 20 entries, sorted by views then name. Paths
are relative to the publish (`/`, `/report.pdf`), with no query or fragment.
Referrers contain only a lowercase hostname, without port, path, query or
credentials. Missing/invalid referrers are omitted, not stored as a host.

**What is stored:** daily counts, per-day hashed visitor membership for exact
deduplication, referrer-host counts and path counts under
`_meta/stats/<slug>/<UTC-date>/<writer>.json` in the existing store. Each server
writes a separate daily shard, and reads merge them without double-counting
daily visitors. Path/referrer maps keep up to 1,000 distinct entries per shard;
additional values fold into `(other)`. Hash membership grows with daily unique
visitors. There are no individual event records or request timestamps. **Raw
IPs, raw user agents, cookies, full referrer URLs and query strings are never
stored by this counter.** There are no analytics cookies or external calls by
default. Existing proxy/access logs are separate and operator-controlled.

Rollups stay until you delete the site (which also removes its stats), delete
old daily prefixes yourself, or apply your object store's lifecycle policy.
There is no automatic retention cutoff. Keep the metadata store private.
Set a stable `OWNNOW_SECRET` on every replica so daily hashes agree. Single
binary mode persists an automatically generated fallback in
`_meta/instance-secret`; initialize once before starting multiple replicas,
or explicitly configure the same secret. Changing the secret during a day
can increase that day's uniques. Store failures log a generic warning and
leave serving available; failed writes are not counted.

Set `OWNNOW_COUNTING=false` to stop new counts; historical stats remain readable.
While counting is on, browsers revalidate site files (`private, no-cache`) so
repeat opens reach the server; conditional 304 requests count too. For large
traffic volumes, account for one rollup read/write per counted request and
rollup scans for stats queries. This is a small-instance counter, not a data
warehouse.

**Behind a proxy:** forwarded headers are ignored by default. Set
`OWNNOW_TRUSTED_PROXIES` to comma-separated proxy CIDRs, for example
`127.0.0.1/32,::1/128` behind a loopback reverse proxy. For Docker/Caddy, use
the actual private Docker network CIDR. The server walks `X-Forwarded-For`
from the trusted peer toward the client, stopping at the first untrusted IP.
Trust only your proxy networks and ensure they append/overwrite forwarded
headers. Otherwise all visitors can appear as the proxy, or clients can spoof
identities. No forwarded IP is stored.

### Optional tracker injection

The counter works without JavaScript. To add richer client analytics, set
`OWNNOW_CONFIG=/path/to/ownnow.json` with an optional `analytics` block. It is
**off by default**. For Docker, mount that config file read-only and set its
container path. No live instance settings are changed by installing/upgrading.

For a self-hosted Umami instance:

```json
{
  "analytics": {
    "provider": "umami",
    "scriptUrl": "https://umami.harssha.design/script.js",
    "websiteId": "YOUR-UMAMI-WEBSITE-ID"
  }
}
```

Create one Umami website for `now.harssha.design` and use its website ID.
Path-mode publishes then appear as paths such as `/my-site/` and
`/my-site/report.html`. There is no website per publish. This example is
configuration guidance only; supply your own URL/ID for another instance.
The separately deployed `ownnow.dev` marketing site uses its own existing tag.

Plausible uses `{"analytics":{"provider":"plausible","scriptUrl":
"https://plausible.example/js/script.js","domain":"now.example.com"}}`.
A custom tracker uses `{"analytics":{"provider":"custom","snippet":
"<script defer src=\"https://metrics.example/tracker.js\"></script>"}}`.
Custom snippets are trusted operator HTML; use them only from a source you trust.

Injection happens in served HTML, leaving uploaded versions untouched. Existing
Umami/Plausible tags, common tracker source markers, or the configured custom
script source/snippet skip injection. This is a conservative tag check, not
universal tracker detection. `ownnow publish ./site --no-analytics` persists an
opt-out for that publish; `--analytics` re-enables injection. Omitted flags keep
the current choice. The API equivalent is `"noAnalytics": true` in the create/
update request. This flag affects injection; built-in counting remains on.

### Cloudflare Worker serving

Traffic served by the optional Worker never reaches the Go server. To include
it, keep a Go instance running against the same R2 bucket and configure:

- Go: `OWNNOW_EDGE_STATS_KEY` with a separate random shared key, and a stable
  `OWNNOW_SECRET` (the existing publishing key is not used by the Worker).
- Worker vars: `STATS_ORIGIN` with the HTTPS URL of that Go instance.
- Worker secrets: `STATS_KEY` matching `OWNNOW_EDGE_STATS_KEY`, and
  `STATS_SECRET` matching `OWNNOW_SECRET`, set with `bunx wrangler secret put`.

The Worker filters requests, hashes the Cloudflare-provided client IP locally,
and asynchronously sends only the daily hash, path, referrer host and file
flag to `POST /api/v1/publishes/<slug>/stats/edge`. Raw IPs and UAs never cross
the bridge. Edge cache hits also count. Failed reports are not retried, so an
unreachable Go instance can undercount. Without the bridge, the Worker serves
normally and cannot add built-in counts. `COUNTING="false"` disables Worker
reports; `OWNNOW_COUNTING=false` on Go also rejects new counts from the bridge.

For optional Worker-side injection set `ANALYTICS` to the analytics block's
JSON value, e.g.
`{"provider":"umami","scriptUrl":"https://metrics.example/script.js","websiteId":"YOUR-ID"}`. It follows the persisted per-publish
opt-out and skips existing trackers. Worker cache entries store the original
HTML; injection and reporting run per request. The authenticated stats API/CLI
always targets the Go instance. No Worker deployment is required for Go serving.

## Auth model

Deliberately boring. **One bearer key** (`OWNNOW_API_KEY`) guards
publishing. You generate it. There are no accounts, no hosted control
plane, and no external telemetry by default. Nothing phones home. Without a key, the
server runs in anonymous mode: sites expire after 24 hours and a claim
token guards updates. That is good for demos, not for production.

## FAQ

**How is it different from here.now?** here.now is a hosted service.
ownnow is software you run yourself, with no accounts, no hosted
control plane, and no vendor telemetry. Some here.now features, like email
allowlists and teams, are not built yet.

**What does it cost to run?** The software is free. You pay for your
own server and bandwidth. On Cloudflare, R2 has no egress fees and the
serving Worker fits the free tier.

**What can I publish?** Static files: HTML sites, images, PDFs, and any
other file type. Single-page apps, custom 404 pages, and Wasm games
work. ownnow does not run server-side code.

**Do I need my own domain?** For public sites, yes. Point an A record
at your server and Caddy handles TLS. For local work, the single
binary runs on localhost.

More answers on [ownnow.dev](https://ownnow.dev/#faq).

## Architecture

- **Server and CLI: Go**, stdlib only, `CGO_ENABLED=0`, one static
  binary. Even the S3 SigV4 signing, including presigned URLs, is
  hand-rolled in `internal/store`. No AWS SDK.
- **Storage:** local disk or any S3-compatible store, with R2
  first-class. Site metadata is JSON next to the files. Versions are
  immutable prefixes, so finalize and rollback are pointer flips.
- **Edge (optional):** a TypeScript Worker (`worker/`, Bun and
  Wrangler) reads the same R2 layout. Rust was evaluated and skipped:
  the Worker is about 150 lines of fetch logic where it buys nothing.

## Roadmap

**Done:** core publish flow, versions and rollback, password sites,
TTL, SPA fallback, custom 404, Wasm, cross-origin isolation, and built-in analytics.
**Not yet:** email allowlists, site data and forms,
custom-domain mounts, folders and search, teams. PRs welcome.

## Development

```bash
go test ./...
python3 -m unittest discover -s skill/scripts/tests
cd worker && bun install && bunx tsc -p tsconfig.json && bun test ./tests
```

The website lives in [`examples/demo-site`](examples/demo-site) and
deploys to [ownnow.dev](https://ownnow.dev) on Vercel from `main`.

## License

MIT. See [LICENSE](LICENSE).

---

Made by [Harssha Vardan](https://harssha.design) ·
[X](https://x.com/Harsshavardan3) ·
[GitHub](https://github.com/harssha6) ·
[LinkedIn](https://www.linkedin.com/in/harssha-vardan-a031371a4/)
