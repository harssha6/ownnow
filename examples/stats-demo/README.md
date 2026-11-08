# Try stats with an HTML page and a PDF

Build ownnow with `go build -o ownnow ./cmd/ownnow`. From the repository root,
start a local-only server in one terminal. This example key is for loopback
use only; generate a random key for a public server.

```bash
OWNNOW_ADDR=127.0.0.1:8080 OWNNOW_API_KEY=local-demo-key \
  OWNNOW_DATA_DIR=./data/stats-demo ./ownnow serve
```

In a second terminal, publish the example folder and make two normal requests:

```bash
export OWNNOW_SERVER=http://127.0.0.1:8080
export OWNNOW_API_KEY=local-demo-key
./ownnow publish examples/stats-demo --slug stats-demo

curl -fsS -A 'Mozilla/5.0 Firefox/140.0' -H 'Accept: text/html' \
  "$OWNNOW_SERVER/stats-demo/" -o /dev/null
curl -fsS -A 'Mozilla/5.0 Firefox/140.0' -H 'Accept: application/pdf' \
  "$OWNNOW_SERVER/stats-demo/report.pdf" -o /dev/null

./ownnow stats stats-demo --days 7
python3 skill/scripts/stats.py stats-demo --days 7
curl -fsS -H "Authorization: Bearer $OWNNOW_API_KEY" \
  "$OWNNOW_SERVER/api/v1/publishes/stats-demo/stats?days=7"
```

On a fresh data directory, all three commands report these totals in their
JSON response. Repeating the requests increases views while the same IP and
user agent remain one unique visitor for the UTC day.

```json
{"totals": {"views": 2, "uniqueVisitors": 1, "fileOpens": 1}}
```

These bot and privacy-opt-out requests leave those totals unchanged:

```bash
curl -fsS -A 'Googlebot/2.1' -H 'Accept: application/pdf' \
  "$OWNNOW_SERVER/stats-demo/report.pdf" -o /dev/null
curl -fsS -A 'Mozilla/5.0 Firefox/140.0' -H 'Accept: text/html' -H 'DNT: 1' \
  "$OWNNOW_SERVER/stats-demo/" -o /dev/null
```

`curl` is normally filtered as an automated user agent, so the first two
requests deliberately use a normal browser UA. The counter counts request
opens, not completed downloads; several Range requests can count more than once.

Optional tracker injection is off in this default server. If an operator has
enabled it, opt this publish out with either client:

```bash
./ownnow publish examples/stats-demo --slug stats-demo --no-analytics
python3 skill/scripts/publish.py examples/stats-demo --slug stats-demo --no-analytics
```

`--analytics` re-enables injection; omitted flags keep the saved choice. These
flags leave built-in server counting on. To stop new counts instance-wide,
stop the local server and restart it with the same data directory/key and
`OWNNOW_COUNTING=false`. History stays readable. Unset `OWNNOW_CONFIG` or
remove its `analytics` block to disable optional injection instance-wide.

The counter stores daily totals, per-day salted visitor hashes, referrer hosts
and paths in your local/S3/R2 store. It stores no raw IPs, raw user agents,
cookies, query strings, full referrers or individual request logs. Deleting the
publish removes its rollups. See [the full contract](../../docs/stats-and-analytics.md)
for retention, proxy setup and the optional Worker bridge.
