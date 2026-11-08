import { afterAll, expect, test } from "bun:test";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { countable, reportHit } from "../src/analytics";

const normalHeaders = {
  "User-Agent": "Mozilla/5.0 Firefox/140.0",
  Accept: "text/html,application/pdf,*/*",
};
test("bot, privacy and subresource filtering", () => {
  const req = (headers: Record<string, string>) =>
    new Request("https://pages.example/report/", {
      headers: { ...normalHeaders, ...headers },
    });
  expect(countable(req({}))).toBe(true);
  for (const headers of [
    { "User-Agent": "Googlebot" },
    { Accept: "" },
    { DNT: "1" },
    { "Sec-GPC": "1" },
    { "Sec-Fetch-Dest": "image" },
  ])
    expect(countable(req(headers))).toBe(false);
});
test("bridge sends a daily hash and sanitized fields, without IP/UA", async () => {
  const originalFetch = globalThis.fetch;
  let payload: any;
  globalThis.fetch = (async (_url: any, init: any) => {
    payload = JSON.parse(init.body);
    expect(init.headers.Authorization).toBe("Bearer edge-key");
    return new Response(null, { status: 204 });
  }) as typeof fetch;
  try {
    const req = new Request(
      "https://pages.example/report/report.pdf?secret=hidden",
      {
        headers: {
          ...normalHeaders,
          "CF-Connecting-IP": "203.0.113.9",
          Referer: "https://news.example/private?token=secret",
        },
      },
    );
    const env = {
      STATS_ORIGIN: "https://control.example",
      STATS_KEY: "edge-key",
      STATS_SECRET: "shared-secret",
    };
    await reportHit(
      req,
      env,
      "report",
      "report.pdf",
      new Response("PDF", { headers: { "Content-Type": "application/pdf" } }),
    );
    expect(payload.path).toBe("/report.pdf");
    expect(payload.referrer).toBe("news.example");
    expect(payload.file).toBe(true);
    expect(payload.visitorHash.length).toBe(64);
    expect(Object.keys(payload).sort()).toEqual([
      "date",
      "file",
      "path",
      "referrer",
      "visitorHash",
    ]);
    const first = payload.visitorHash;
    await reportHit(
      req,
      env,
      "report",
      "report.pdf",
      new Response("PDF", { headers: { "Content-Type": "application/pdf" } }),
    );
    expect(payload.visitorHash).toBe(first);
    expect(JSON.stringify(payload).includes("203.0.113.9")).toBe(false);
    expect(JSON.stringify(payload).includes("Mozilla")).toBe(false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

let runtime: Miniflare | undefined;
// Bun's fetch ignores undici's custom dispatchers. Use Miniflare's actual HTTP
// entrypoint so this exercises workerd/HTMLRewriter under Bun too.
async function runtimeFetch(
  target: string,
  init?: RequestInit,
): Promise<Response> {
  const url = new URL(new URL(target).pathname, await runtime!.ready);
  return fetch(url, init);
}
afterAll(async () => {
  await runtime?.dispose();
});
test("Worker runtime injects once, skips existing and opted-out trackers, serves PDF and cached HTML", async () => {
  const build = await Bun.build({
    entrypoints: [new URL("../src/index.ts", import.meta.url).pathname],
    target: "browser",
  });
  if (!build.success) throw new Error(String(build.logs));
  const script = await build.outputs[0].text();
  runtime = new Miniflare(
    convertV4MiniflareOptions({
      modules: true,
      script,
      compatibilityDate: "2025-01-01",
      r2Buckets: ["SITES"],
      bindings: {
        BASE_DOMAIN: "pages.example",
        ANALYTICS: JSON.stringify({
          provider: "umami",
          scriptUrl: "https://metrics.example/script.js",
          websiteId: "id",
        }),
      },
    }),
  );
  const bucket = await runtime.getR2Bucket("SITES");
  for (const [slug, noAnalytics, body] of [
    ["report", false, "<html><head></head><body>Hello</body></html>"],
    [
      "existing",
      false,
      '<script data-website-id="already" src="/metrics.js"></script>',
    ],
    ["optout", true, "<head></head><body>private</body>"],
  ] as const) {
    await bucket.put(
      `_meta/sites/${slug}.json`,
      JSON.stringify({ slug, currentVersion: "v1", noAnalytics }),
    );
    await bucket.put(`sites/${slug}/v1/index.html`, body);
    await bucket.put(`sites/${slug}/v1/report.pdf`, "%PDF-1.4 Test PDF");
  }
  for (let i = 0; i < 2; i++) {
    const res = await runtimeFetch("https://pages.example/report/", {
      headers: normalHeaders,
    });
    expect(res.status).toBe(200);
    expect(res.headers.get("Cache-Control")).toBe("private, no-cache");
    const html = await res.text();
    expect(html.split("data-ownnow-analytics").length - 1).toBe(1);
  }
  for (const [slug, headers] of [
    ["existing", normalHeaders],
    ["optout", normalHeaders],
    ["report", { ...normalHeaders, DNT: "1" }],
    ["report", { ...normalHeaders, "Sec-GPC": "1" }],
  ] as const) {
    const res = await runtimeFetch(`https://pages.example/${slug}/`, {
      headers,
    });
    expect((await res.text()).includes("data-ownnow-analytics")).toBe(false);
  }
  const pdf = await runtimeFetch("https://pages.example/report/report.pdf", {
    headers: normalHeaders,
  });
  expect(pdf.headers.get("Content-Type")).toBe("application/pdf");
  expect(await pdf.text()).toBe("%PDF-1.4 Test PDF");
}, 60000);
