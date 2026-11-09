# Harssha instance: enable self-hosted Umami

**This is an opt-in deployment configuration for Harssha's instance only.**
Generic Docker, single-binary and Worker installs leave tracker injection off.
Merging or building the package alone does not load this overlay. These files
prepare a later deployment; they have not been applied to the live server.

Two existing Umami websites are reused; no website or live Umami settings are
created or changed by this configuration:

| Public domain | Existing website ID | Delivery |
| --- | --- | --- |
| `now.harssha.design` | `f8917691-72ac-4458-87eb-c93d7298c6c8` | Go server injection from `analytics.json` |
| `ownnow.dev` | `39c49169-9784-4529-a196-286ad0008b43` | Static Vercel HTML tags in `examples/demo-site/` |

The IDs are public tracker identifiers, not credentials. Both use
`https://umami.harssha.design/script.js`. Domain filters keep local/tailnet and
Vercel preview traffic out of the production Umami websites. One website for
`now.harssha.design` collects all publishes as paths, including `/my-site/`.

## What changes when this server configuration is deployed

The `ownnow` service will build the reviewed checkout, mount `analytics.json`
read-only at `/etc/ownnow/analytics.json`, and set `OWNNOW_CONFIG` to that file.
It will inject one Umami tag into served HTML that has no recognized existing
tracker and has not opted out. Existing publishes get this behavior without
republishing. `--no-analytics`, `DNT: 1` and `Sec-GPC: 1` skip injection. Uploaded
versions and PDFs/images/downloads are not rewritten.

The injected script has the existing `now.harssha.design` website ID and
`data-domains="now.harssha.design"`. The browser sends HTML page events to the
self-hosted Umami collector. The independent server counter also starts writing
new daily JSON rollups into the existing S3/R2 store when the new binary is
deployed; it is explicitly enabled here. It counts direct non-HTML file opens,
which the Umami script cannot see. There is no historical backfill. The stats
API stays authenticated. Documents, images and downloads revalidate while server counting is on;
CSS/JS/fonts/JSON retain normal caching. Counts are batched off the serving
path and can be lost during overload, storage failure or abrupt termination.

`OWNNOW_TRUSTED_PROXIES=172.21.0.0/16` trusts the current private
`ownnow-deploy_default` Docker network so the counter can derive the real
visitor IP from Caddy's `X-Forwarded-For` chain before hashing it. Verify the
network before deployment and update this instance overlay if it changed.
Neither this file nor the counter stores the raw IP or user agent.

Existing API keys, storage credentials, domain routing, Caddy, Umami and its
database stay in the existing deployment. No secret values belong in these
files. The server changes require the explicit overlay command below; the
normal generic Compose file continues to leave injection off.

## What changes on ownnow.dev

`ownnow.dev` is a separate static Vercel site, not a route on the Go server.
Its homepage already has the self-hosted Umami tag, and that tag remains in
place. This instance commit also adds the same tag and domain filter to its
`404.html`. A later Vercel production deployment will therefore keep homepage
tracking and add 404-page tracking. It will not connect this static site to the
Go rollup counter. The built-in counter serves published sites on
`now.harssha.design`; the marketing site's analytics remain in Umami.

## Review or deploy later

From the reviewed ownnow checkout, validate the overlay without changing any
running service. `config --quiet` validates without printing expanded secrets:

```bash
export OWNNOW_SOURCE="$(pwd)"
docker network inspect ownnow-deploy_default --format '{{json .IPAM.Config}}'
docker compose --project-directory /home/clawd/ownnow-deploy \
  -f /home/clawd/ownnow-deploy/docker-compose.yml \
  -f "$OWNNOW_SOURCE/deploy/harssha/compose.analytics.yml" config --quiet
```

After deployment is approved, the operator can build/recreate only ownnow:

```bash
docker compose --project-directory /home/clawd/ownnow-deploy \
  -f /home/clawd/ownnow-deploy/docker-compose.yml \
  -f "$OWNNOW_SOURCE/deploy/harssha/compose.analytics.yml" \
  up -d --build --no-deps ownnow
```

This command is documented for the operator; preparing this PR does not run it.
It keeps the existing deploy directory's `.env` and the current Caddy service.
The overlay must remain in future Compose commands to retain its settings.
The production website is deployed through its existing Vercel Git connection;
this PR does not merge or trigger a production deployment.

After deployment, inspect a normal HTML response for one tracker tag, check a
publish with `--no-analytics`, and confirm one deliberate browser test visit in
the appropriate Umami website. Do not interpret a bot/curl visit as proof that
a client-side event reached Umami.

## Turn it off or roll it back

To stop injection while keeping this binary and its server counters, remove the
`analytics` block from the mounted config, or remove `OWNNOW_CONFIG` from the
instance overlay, then recreate ownnow with the reviewed config. Existing
tracker scripts in uploaded HTML remain the publisher's responsibility.
`--no-analytics` disables only injected trackers for a single publish.

To stop server counting too, set `OWNNOW_COUNTING=false` and recreate ownnow;
historical rollups remain readable. Removing the overlay removes its injected
tracker configuration and trusted-proxy setting on the next container recreate;
counting returns to the package default (on). To undo the website change,
revert its 404 tag and redeploy the static site. Existing Umami data and server
rollups are not deleted by either rollback.
