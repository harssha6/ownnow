// Server counting happens on the Go control plane, including edge-cache hits.
// The Worker sends only a daily HMAC and sanitized dimensions, never IP/UA.
export interface StatsEnv {
  STATS_ORIGIN?: string;
  STATS_KEY?: string;
  STATS_SECRET?: string;
  COUNTING?: string;
  ANALYTICS?: string; // same JSON analytics block as the Go instance
}
export function countable(req: Request): boolean {
  if (
    req.method !== "GET" ||
    req.headers.get("DNT") === "1" ||
    req.headers.get("Sec-GPC") === "1" ||
    !req.headers.get("Accept")?.trim()
  )
    return false;
  const ua = req.headers.get("User-Agent")?.toLowerCase() ?? "";
  if (
    !ua ||
    [
      "bot",
      "spider",
      "crawler",
      "slurp",
      "headless",
      "facebookexternalhit",
      "preview",
      "lighthouse",
      "wget",
      "curl/",
      "python-requests",
      "go-http-client",
      "uptime",
      "monitor",
      "scanner",
      "scrapy",
    ].some((s) => ua.includes(s))
  )
    return false;
  return ["", "empty", "document", "iframe"].includes(
    req.headers.get("Sec-Fetch-Dest") ?? "",
  );
}
async function hmac(
  key: Uint8Array<ArrayBuffer>,
  text: string,
): Promise<Uint8Array<ArrayBuffer>> {
  const k = await crypto.subtle.importKey(
    "raw",
    key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return new Uint8Array(
    await crypto.subtle.sign("HMAC", k, new TextEncoder().encode(text)),
  );
}
export async function reportHit(
  req: Request,
  env: StatsEnv,
  slug: string,
  rest: string,
  res: Response,
): Promise<void> {
  if (
    env.COUNTING === "false" ||
    !env.STATS_ORIGIN ||
    !env.STATS_KEY ||
    !env.STATS_SECRET ||
    !countable(req) ||
    ![200, 206, 304].includes(res.status)
  )
    return;
  const date = new Date().toISOString().slice(0, 10);
  const salt = await hmac(
    new TextEncoder().encode(env.STATS_SECRET),
    `ownnow-stats\0${date}\0${slug}`,
  );
  const hash = await hmac(
    salt,
    `${req.headers.get("CF-Connecting-IP") ?? ""}\0${req.headers.get("User-Agent") ?? ""}`,
  );
  let referrer = "";
  try {
    const u = new URL(req.headers.get("Referer") ?? "");
    if (["http:", "https:"].includes(u.protocol))
      referrer = u.hostname.toLowerCase();
  } catch {
    /* direct */
  }
  const url = new URL(
    `/api/v1/publishes/${encodeURIComponent(slug)}/stats/edge`,
    env.STATS_ORIGIN,
  );
  try {
    const result = await fetch(url, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${env.STATS_KEY}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        date,
        visitorHash: [...hash]
          .map((b) => b.toString(16).padStart(2, "0"))
          .join(""),
        path: rest.length > 2047 ? "/(other)" : "/" + rest,
        referrer,
        file: !(res.headers.get("Content-Type") ?? "")
          .toLowerCase()
          .startsWith("text/html"),
      }),
      signal: AbortSignal.timeout(5000),
      redirect: "error",
    });
    await result.body?.cancel();
    if (!result.ok) console.warn("ownnow: edge stats rejected");
  } catch {
    console.warn("ownnow: could not persist edge stats");
  }
}

type Analytics = {
  provider?: string;
  scriptUrl?: string;
  websiteId?: string;
  domain?: string;
  snippet?: string;
};
function escapeAttr(s: string): string {
  return s
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}
export async function injectAnalytics(
  res: Response,
  req: Request,
  env: StatsEnv,
  noAnalytics?: boolean,
): Promise<Response> {
  if (
    !env.ANALYTICS ||
    noAnalytics ||
    req.method === "HEAD" ||
    req.headers.get("DNT") === "1" ||
    req.headers.get("Sec-GPC") === "1" ||
    !(res.headers.get("Content-Type") ?? "").startsWith("text/html") ||
    res.status !== 200
  )
    return res;
  let a: Analytics;
  try {
    a = JSON.parse(env.ANALYTICS) as Analytics;
  } catch {
    console.warn("ownnow: invalid ANALYTICS config");
    return res;
  }
  let snippet = "";
  if (a.provider === "custom") snippet = a.snippet ?? "";
  else if (
    (a.provider === "umami" && a.websiteId) ||
    (a.provider === "plausible" && a.domain)
  ) {
    let url: URL;
    try {
      url = new URL(a.scriptUrl ?? "");
    } catch {
      return res;
    }
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password
    )
      return res;
    snippet = `<script defer data-ownnow-analytics src="${escapeAttr(a.scriptUrl!)}" ${a.provider === "umami" ? `data-website-id="${escapeAttr(a.websiteId!)}"` : `data-domain="${escapeAttr(a.domain!)}"`}></script>`;
  }
  if (!snippet) return res;
  const source = await res.text();
  const headers = new Headers(res.headers);
  headers.delete("Content-Length");
  headers.delete("ETag");
  headers.append("Vary", "DNT, Sec-GPC");
  let present = a.provider === "custom" && source.includes(snippet);
  const customSources = new Set<string>();
  if (a.provider === "custom")
    await new HTMLRewriter()
      .on("script[src]", {
        element(el) {
          customSources.add(el.getAttribute("src")!);
        },
      })
      .transform(new Response(snippet))
      .text();
  await new HTMLRewriter()
    .on("script", {
      element(el) {
        const src = el.getAttribute("src") ?? "";
        if (
          ["data-ownnow-analytics", "data-website-id", "data-domain"].some(
            (k) => el.hasAttribute(k),
          ) ||
          (src && (src === a.scriptUrl || customSources.has(src))) ||
          [
            "umami",
            "plausible",
            "googletagmanager.com",
            "google-analytics.com",
            "matomo",
            "posthog",
            "static.cloudflareinsights.com/beacon",
          ].some((s) => src.toLowerCase().includes(s))
        )
          present = true;
      },
    })
    .transform(new Response(source))
    .text();
  let inserted = false;
  const result = await new HTMLRewriter()
    .on("head, body", {
      element(el) {
        if (!present && !inserted) {
          el.append(snippet, { html: true });
          inserted = true;
        }
      },
    })
    .transform(new Response(source))
    .text();
  return new Response(!present && !inserted ? result + snippet : result, {
    status: res.status,
    headers,
  });
}
