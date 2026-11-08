"""Exercise skill publish flags and stats requests at the API boundary."""
import contextlib
import io
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
import publish
import stats


class AnalyticsClientTest(unittest.TestCase):
    def test_publish_sends_explicit_opt_out_and_preserves_omitted_setting(self):
        for flags, expected in [([], None), (["--no-analytics"], True), (["--analytics"], False)]:
            with self.subTest(flags=flags), tempfile.TemporaryDirectory() as folder:
                page = pathlib.Path(folder) / "index.html"
                page.write_text("<h1>Report</h1>")
                created = {"slug": "report", "versionId": "v1", "siteUrl": "https://pages.example/report/", "uploads": []}
                with patch.object(sys, "argv", ["publish.py", str(page), "--slug", "report", "--no-share-preview", *flags]), \
                     patch.object(publish, "api_key", return_value="test-key"), \
                     patch.object(publish, "existing_site", return_value={"indexable": False}), \
                     patch.object(publish, "call", side_effect=[created, {"siteUrl": created["siteUrl"]}]) as api, \
                     contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    publish.main()
                method, path, key, body = api.call_args_list[0].args
                self.assertEqual((method, path, key), ("PUT", "/api/v1/publish/report", "test-key"))
                if expected is None:
                    self.assertNotIn("noAnalytics", body)
                else:
                    self.assertIs(body["noAnalytics"], expected)

    def test_stats_uses_publish_credentials_and_prints_api_json(self):
        result = {"slug": "report", "totals": {"views": 2, "uniqueVisitors": 1, "fileOpens": 1}}
        out = io.StringIO()
        with patch.object(stats, "api_key", return_value="test-key"), \
             patch.object(stats, "call", return_value=result) as api, contextlib.redirect_stdout(out):
            stats.main(["report", "--days", "3"])
        api.assert_called_once_with("GET", "/api/v1/publishes/report/stats?days=3", "test-key")
        self.assertEqual(json.loads(out.getvalue()), result)

    def test_stats_rejects_invalid_period_before_network(self):
        for value in ["0", "366", "not-a-number"]:
            with self.subTest(value=value), patch.object(stats, "call") as api, \
                 contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                stats.main(["report", "--days", value])
            self.assertEqual(error.exception.code, 2)
            api.assert_not_called()

    def test_saved_claim_is_forwarded_without_an_api_key(self):
        with patch.dict(publish.os.environ, {"OWNNOW_CLAIM": "claim-test"}), \
             patch.object(publish.urllib.request, "urlopen") as open_url:
            response = open_url.return_value.__enter__.return_value
            response.headers.get_content_type.return_value = "application/json"
            response.read.return_value = b'{"totals": {"views": 0}}'
            publish.call("GET", "/api/v1/publishes/report/stats", "")
            request = open_url.call_args.args[0]
            self.assertEqual(request.get_header("X-ownnow-claim"), "claim-test")
            self.assertIsNone(request.get_header("Authorization"))
