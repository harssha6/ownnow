---
name: ownnow
description: >
  Publish files and folders to the web instantly on your own ownnow server.
  Static hosting for HTML sites, images, PDFs, and any file type. Use when
  asked to "publish this", "host this", "deploy this", "share this on the
  web", "make a website", "put this online", "upload to the web",
  "create a webpage", "share a link", "serve this site", or
  "generate a URL". Outputs a live, shareable URL from your ownnow instance.
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

## Publish

```bash
python3 scripts/publish.py {file-or-dir} [--slug name] [--password pw] [--ttl-hours n] [--spa]
```

Prints the live URL (e.g. `https://now.example.com/bright-canvas-a7k2/`).
For HTML sites, `index.html` must be at the root of the folder you publish.
Updates: publish again with the same `--slug` (every publish is a new
version; old versions are kept for rollback). Without an API key the
script still works against servers running in anonymous mode, and prints
a claim token you must save to update/delete that site later.

## Manage

With `OWNNOW_API_KEY` set, against `$OWNNOW_SERVER/api/v1`:

- `GET /publishes` — list sites
- `GET /publish/{slug}` / `GET /publish/{slug}/versions` — detail, history
- `POST /publish/{slug}/rollback {"versionId":"v_..."}` — roll back
- `PATCH /publish/{slug}/metadata` — name, password, TTL, SPA mode
- `DELETE /publish/{slug}` — delete
