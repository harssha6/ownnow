# ownnow

**Your own [here.now](https://here.now), on your own infra.**

Publish a file or folder → get a live URL. That's it. No account with a
hosting company, no SaaS in the middle: one Go binary, your domain, your
storage. Built because here.now is a great product with no self-host /
bring-your-own-infra option — this is that option.

```bash
ownnow publish ./my-site
# -> https://now.example.com/bright-canvas-a7k2/
```

![status](https://img.shields.io/badge/license-MIT-green) — static sites,
images, PDFs, Wasm apps, any file type.

## Bring your own infra — pick one

| Path | You need | Serving cost |
|---|---|---|
| **Docker anywhere** (default) | Any VPS/host + a domain | Your host's bandwidth |
| **Cloudflare** | A domain zone on Cloudflare + R2 | **$0 egress** (R2) + Workers free tier |
| **Single binary** | Nothing (dev) | localhost only |

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
`OWNNOW_STORAGE=s3` to use R2/S3/MinIO instead.

### 2. Cloudflare (zero-egress serving)

The Go binary becomes publish-only control plane (run it anywhere,
even a laptop); a TypeScript Worker serves traffic from R2 at the edge:

```bash
cd worker && bun install
bunx wrangler r2 bucket create ownnow
cp wrangler.example.toml wrangler.toml   # your domain + bucket
bun run deploy                            # wildcard {slug}.your-domain
```

Run the Go server with `OWNNOW_STORAGE=s3` + your R2 keys (see
`.env.example`). Uploads are presigned, so file bytes never pass
through the server either.

### 3. Single binary

```bash
go build -o ownnow ./cmd/ownnow
OWNNOW_API_KEY=$(openssl rand -hex 32) ./ownnow serve   # :8080, local disk
```

## Publishing

Same 3-step flow agents already know from here.now (create → presigned
upload → finalize; a site is never live until finalize succeeds):

```bash
ownnow publish ./site [--slug name] [--password pw] [--ttl-hours 24] [--spa]
ownnow list
ownnow rollback <slug> <version-id>
ownnow delete <slug>
```

Features: version history + rollback, password-protected sites,
anonymous 24h sites with claim tokens, TTL/expiry, SPA fallback,
custom `404.html`, directory listings, correct `application/wasm` +
COOP/COEP for Wasm/games, Range requests.

**For AI agents:** an agent skill lives in [`skill/`](skill/) — point
Claude/Cursor/etc. at it and "publish this" just works against your
instance (`OWNNOW_SERVER` + `OWNNOW_API_KEY` env vars).

## Auth model

Deliberately boring: **one bearer key** (`OWNNOW_API_KEY`) guards
publishing. You generate it; there are no accounts, no hosted control
plane, no telemetry, nothing phoning home. Without a key, the server
runs in anonymous mode where sites expire in 24h and a claim token
guards updates — good for demos, not for production.

## Architecture

- **Server + CLI — Go**, stdlib only, `CGO_ENABLED=0`, one static
  binary. Even the S3 SigV4 signing (incl. presigned URLs) is
  hand-rolled in `internal/store` — no AWS SDK.
- **Storage** — local disk or any S3-compatible store (R2 first-class).
  Site metadata is JSON next to the files; versions are immutable
  prefixes, so finalize/rollback is a pointer flip.
- **Edge (optional)** — TypeScript Worker (`worker/`, Bun + Wrangler)
  reading the same R2 layout. Rust was evaluated and skipped: the
  Worker is ~150 lines of fetch logic where it buys nothing.

## Roadmap (here.now parity, honestly tracked)

Done: core publish flow, versions/rollback, password sites, TTL, SPA,
404, Wasm/COI. Not yet: restricted email allowlists, site data/forms,
analytics, custom-domain mounts, folders/search, teams. PRs welcome.

## Development

```bash
go test ./...
cd worker && bun install && bunx tsc -p tsconfig.json
```

## License

MIT. See [LICENSE](LICENSE).
