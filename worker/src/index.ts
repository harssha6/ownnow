import { reportHit, injectAnalytics, revalidateForStats, type StatsEnv } from "./analytics";

/**
 * ownnow edge Worker — the ONLY public serving path. Files are served from
 * R2 (zero egress fees) behind the Cloudflare cache. No Vercel, no origin.
 *
 * Site metadata is read from R2 itself — `_meta/sites/{slug}.json`, exactly
 * what the Go publisher writes — so there is no D1 copy to keep in sync.
 * Publishing (Go binary, presigned uploads to R2) can run anywhere,
 * including a laptop: it is control-plane only and serves no public traffic.
 *
 * Run/deploy with Bun:  bun install && bunx wrangler dev | deploy
 */

export interface Env extends StatsEnv {
  SITES: R2Bucket;
  BASE_DOMAIN: string;
}

type SiteMeta = {
  slug: string;
  createdAt: string;
  currentVersion?: string;
  spa?: boolean;
  crossOriginIsolation?: boolean;
  expiresAt?: string;
  indexable?: boolean;
  noAnalytics?: boolean;
};
type SiteSecrets = { PasswordHash?: string };

const MIME: Record<string, string> = {
  html: "text/html; charset=utf-8", css: "text/css", js: "text/javascript", mjs: "text/javascript",
  json: "application/json", wasm: "application/wasm", png: "image/png", jpg: "image/jpeg",
  svg: "image/svg+xml", pdf: "application/pdf", txt: "text/plain; charset=utf-8", md: "text/markdown",
  woff: "font/woff", woff2: "font/woff2", ttf: "font/ttf", otf: "font/otf",
};

function contentType(path: string): string {
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  return MIME[ext] ?? "application/octet-stream";
}

async function sha256Hex(s: string): Promise<string> {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export default {
  async fetch(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(req.url);
    if (!["GET", "HEAD", "POST"].includes(req.method)) return new Response("method not allowed", { status: 405 });
    if (url.pathname === "/healthz") return Response.json({ ok: true, edge: true, serving: "r2" });

    // --- slug resolution: {slug}.BASE_DOMAIN, else /{slug}/... ---
    let slug = "";
    let rest = url.pathname.replace(/^\//, "");
    const host = url.hostname;
    if (env.BASE_DOMAIN && host.endsWith("." + env.BASE_DOMAIN)) {
      slug = host.slice(0, -(env.BASE_DOMAIN.length + 1));
    } else if (rest) {
      const i = rest.indexOf("/");
      slug = i >= 0 ? rest.slice(0, i) : rest;
      rest = i >= 0 ? rest.slice(i + 1) : "";
    }
    if (!slug || slug.includes(".")) return new Response("ownnow — publish something: ownnow publish ./site", { status: 200 });

    // --- metadata from R2 (written by the Go publisher at finalize) ---
    const metaObj = await env.SITES.get(`_meta/sites/${slug}.json`);
    if (!metaObj) return new Response("not found", { status: 404 });
    const site = (await metaObj.json()) as SiteMeta;
    if (!site.currentVersion) return new Response("not found", { status: 404 });
    const headers = new Headers();
    // same tri-state as the Go server: only an explicit false sends noindex
    if (site.indexable === false) headers.set("X-Robots-Tag", "noindex, nofollow, noarchive");
    if (site.expiresAt && new Date(site.expiresAt) < new Date())
      return new Response("this site has expired", { status: 410, headers });

    if (site.crossOriginIsolation) {
      headers.set("Cross-Origin-Opener-Policy", "same-origin");
      headers.set("Cross-Origin-Embedder-Policy", "require-corp");
    }

    // --- password gate (hash lives in _meta/secrets/, never in site JSON) ---
    const secObj = await env.SITES.get(`_meta/secrets/${slug}.json`);
    const secrets = secObj ? ((await secObj.json()) as SiteSecrets) : {};
    if (secrets.PasswordHash) {
      const cookie = req.headers.get("cookie") ?? "";
      const expected = await sha256Hex(`unlock|${slug}|${secrets.PasswordHash}`);
      if (!cookie.includes(`ownnow_unlock_${slug}=${expected}`)) {
        if (req.method === "POST" && url.pathname.endsWith("/__unlock")) {
          const { password } = (await req.json().catch(() => ({}))) as { password?: string };
          if (password && (await sha256Hex(password)) === secrets.PasswordHash) {
            return new Response(null, {
              status: 204,
              headers: { "Set-Cookie": `ownnow_unlock_${slug}=${expected}; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400` },
            });
          }
          return new Response("wrong password", { status: 401 });
        }
        return new Response("🔒 password protected — POST {password} to /__unlock", { status: 401, headers });
      }
    }

    if (req.method === "POST") return new Response("method not allowed", { status: 405 });

    const finish = async (res: Response): Promise<Response> => {
      ctx.waitUntil(reportHit(req, env, slug, rest, res, site.createdAt));
      const injected = await injectAnalytics(res,req,env,site.noAnalytics);
      const result = new Response(injected.body, injected);
      if (env.COUNTING !== "false" && revalidateForStats(result.headers.get("Content-Type") ?? "")) result.headers.set("Cache-Control", "private, no-cache");
      return result;
    };

    const prefix = `sites/${slug}/${site.currentVersion}/`;
    const wanted = rest === "" ? "index.html" : rest;

    // versioned R2 keys are immutable -> cache aggressively at the edge
    const cache = (caches as unknown as { default: Cache }).default;
    const cacheURL = new URL(url);
    cacheURL.searchParams.set("__ownnow_version", site.currentVersion);
    const cacheKey = new Request(cacheURL, req);
    const hit = !secrets.PasswordHash && req.method === "GET" ? await cache.match(cacheKey) : undefined;
    if (hit) return finish(hit);

    let servedPath = wanted;
    let obj = await env.SITES.get(prefix + servedPath);
    let status = 200;
    if (!obj && wanted !== "index.html") {
      servedPath = wanted.endsWith("/") ? wanted+"index.html" : wanted+"/index.html";
      obj = await env.SITES.get(prefix+servedPath);
    }
    if (!obj && site.spa) { servedPath = "index.html"; obj = await env.SITES.get(prefix+servedPath); }
    if (!obj) {
      servedPath = "404.html";
      obj = await env.SITES.get(prefix+servedPath);
      status = 404;
    }
    if (!obj) return new Response("not found", { status: 404, headers });

    obj.writeHttpMetadata(headers);
    headers.set("Content-Type", contentType(servedPath));
    headers.set("ETag", obj.httpEtag);
    // here.now parity: wasm revalidates, everything else sits in cache
    headers.set("Cache-Control", wanted.endsWith(".wasm") ? "no-cache" : "public, max-age=60");

    const res = new Response(req.method === "HEAD" ? null : obj.body, { status, headers });
    if (!secrets.PasswordHash && status === 200 && req.method === "GET") ctx.waitUntil(cache.put(cacheKey, res.clone()));
    return finish(res);
  },
};
