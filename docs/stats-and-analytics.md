# Stats and analytics

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
python3 skill/scripts/stats.py my-site --days 7
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
Unset `OWNNOW_CONFIG` or remove its `analytics` block to disable injection
instance-wide. `--no-analytics` disables injection for one publish, while
`--analytics` re-enables it. Those flags never disable built-in counting.
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
    "websiteId": "YOUR-UMAMI-WEBSITE-ID",
    "domain": "now.example.com"
  }
}
```

The optional `domain` field sets Umami's `data-domains` filter, so previews
served on other hosts do not send events. Set it to your public host.

Create one Umami website for `now.harssha.design` and use its website ID.
Path-mode publishes then appear as paths such as `/my-site/` and
`/my-site/report.html`. There is no website per publish. This example is
configuration guidance only; supply your own URL/ID for another instance.
The separately deployed `ownnow.dev` marketing site uses its own existing tag.
The [Harssha-instance overlay](../deploy/harssha/README.md) reuses both existing
Umami website IDs and opts only that server into injection. Generic Docker,
single-binary and Worker installs do not read it.

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

## Agent commands

The Go CLI and Python skill client use the same API and output schema:

```bash
export OWNNOW_SERVER=https://now.example.com
export OWNNOW_API_KEY=YOUR_PUBLISH_KEY
ownnow stats my-site --days 7
python3 skill/scripts/stats.py my-site --days 7
python3 skill/scripts/publish.py ./site --slug my-site --no-analytics
```

For an anonymous site's stats, set `OWNNOW_CLAIM` to its saved claim token.
Keep keys and claim tokens out of prompts, generated pages and source control.
The Python helpers also honor the installed skill's `local/config.json`.

See [the HTML/PDF walkthrough](../examples/stats-demo/README.md) for a local
publish, normal/bot requests, a stats query and both opt-out switches.
