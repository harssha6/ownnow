"""Run: python3 -m unittest discover -s skill/scripts/tests"""
import contextlib, functools, http.server, io, pathlib, sys, tempfile, threading, unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
import publish, seo, seo_check  # noqa: E402

PAGE = """<!doctype html><html><head><meta charset="utf-8"><title>Field notes</title>
<meta property="og:title" content="Author title"></head>
<body><h1>Field notes</h1><p>A long enough first paragraph that can stand in for a description.</p></body></html>"""


class InjectTest(unittest.TestCase):
    def test_author_tags_win_and_defaults_fill_gaps(self):
        out, added = seo.inject(PAGE, url="https://x/s/", image_url="https://x/s/_ownnow/og.png", indexable=False)
        page = seo.scan(out)
        self.assertEqual(page.meta["og:title"], "Author title")  # untouched
        self.assertEqual(out.count('property="og:title"'), 1)
        self.assertEqual(page.canonical, "https://x/s/")
        self.assertEqual(page.meta["robots"], seo.NOINDEX)
        self.assertEqual(page.meta["twitter:card"], "summary_large_image")
        self.assertTrue(page.meta["description"].startswith("A long enough first paragraph"))
        self.assertLess(out.index("ownnow share defaults"), out.index("</head>"))
        self.assertNotIn("og:title", added)

    def test_unset_and_indexable_add_no_robots_meta(self):
        for mode in (None, True):
            out, _ = seo.inject(PAGE, url="https://x/s/", indexable=mode)
            self.assertNotIn('name="robots"', out)

    def test_existing_robots_meta_is_kept(self):
        src = PAGE.replace("</head>", '<meta name="robots" content="index"></head>')
        out, _ = seo.inject(src, url="https://x/s/", indexable=False)
        self.assertEqual(out.count('name="robots"'), 1)

    def test_fully_tagged_page_is_unchanged(self):
        full, _ = seo.inject(PAGE, url="https://x/s/", image_url="https://x/i.png", indexable=False)
        again, added = seo.inject(full, url="https://x/s/", image_url="https://x/i.png", indexable=False)
        self.assertEqual(again, full)
        self.assertEqual(added, [])

    def test_fragment_without_head(self):
        out, _ = seo.inject("<h1>Hi there</h1>", url="https://x/s/", indexable=False)
        page = seo.scan(out)
        self.assertEqual(page.title, "Hi there")
        self.assertTrue(out.endswith("<h1>Hi there</h1>"))


class StageTest(unittest.TestCase):
    def files(self, tree):
        d = pathlib.Path(tempfile.mkdtemp())
        for rel, text in tree.items():
            (d / rel).parent.mkdir(parents=True, exist_ok=True)
            (d / rel).write_text(text)
        return d, publish.collect(d)

    def test_pages_get_their_own_urls_and_one_image(self):
        d, files = self.files({"index.html": PAGE, "blog/index.html": "<title>Blog</title>",
                               "about.html": "<title>About</title>", "404.html": "<title>nope</title>"})
        out, report = publish.stage(files, "https://x/s/", slug="s", indexable=False)
        self.assertEqual(seo.scan(out["blog/index.html"].decode()).canonical, "https://x/s/blog/")
        self.assertEqual(seo.scan(out["about.html"].decode()).canonical, "https://x/s/about.html")
        self.assertNotIn("404.html", report)
        if seo.can_render():
            self.assertEqual(seo.image_size(out[seo.OG_PATH]), seo.OG_SIZE)
        self.assertEqual((d / "index.html").read_text(), PAGE)  # source untouched

    def test_no_share_preview_uploads_bytes_as_is(self):
        _, files = self.files({"index.html": PAGE})
        out, report = publish.stage(files, "https://x/s/", slug="s", indexable=False, share=False)
        self.assertEqual(out["index.html"].decode(), PAGE)
        self.assertEqual(report, {})

    def test_single_html_file_is_canonical_at_root(self):
        self.assertEqual(publish.page_url("https://x/s/", "deck.html", ["deck.html"]), "https://x/s/")


@unittest.skipUnless(seo.can_render(), "Pillow not installed")
class LocalConfigTest(unittest.TestCase):
    def skill(self, config=None):
        d = pathlib.Path(tempfile.mkdtemp())
        if config is not None:
            (d / "local").mkdir()
            (d / "local" / "config.json").write_text(__import__("json").dumps(config))
            (d / "local" / "key.py").write_text("print('k-from-command')")
        return d

    def test_no_local_folder_keeps_the_generic_defaults(self):
        d = self.skill()
        cfg = publish.local_config(d)
        self.assertEqual(publish.server_url({}, cfg), "http://127.0.0.1:8080")
        self.assertEqual(publish.api_key({}, cfg, d), "")

    def test_local_config_supplies_server_and_key(self):
        d = self.skill({"server": "https://now.example.com/", "keyCommand": ["python3", "local/key.py"]})
        cfg = publish.local_config(d)
        self.assertEqual(publish.server_url({}, cfg), "https://now.example.com")
        self.assertEqual(publish.api_key({}, cfg, d), "k-from-command")

    def test_env_wins_over_local_config(self):
        d = self.skill({"server": "https://now.example.com", "keyCommand": ["false"]})
        env = {"OWNNOW_SERVER": "http://127.0.0.1:9", "OWNNOW_API_KEY": "k-env"}
        cfg = publish.local_config(d)
        self.assertEqual(publish.server_url(env, cfg), "http://127.0.0.1:9")
        self.assertEqual(publish.api_key(env, cfg, d), "k-env")  # command never runs


class ImageTest(unittest.TestCase):
    def test_long_and_non_latin_titles_render(self):
        for title in ("A very long title " * 8, "दिवाली नोट्स", ""):
            self.assertEqual(seo.image_size(seo.og_image(title, "now.example.com/s")), seo.OG_SIZE)


class _Handler(http.server.SimpleHTTPRequestHandler):
    noindex = False

    def end_headers(self):
        if self.noindex:
            self.send_header("X-Robots-Tag", seo.NOINDEX)
        super().end_headers()

    def log_message(self, *a):
        pass


class CheckUrlTest(unittest.TestCase):
    def run_check(self, url, mode):
        with contextlib.redirect_stdout(io.StringIO()) as buf:
            rep = seo_check.check_url(url, mode)
        return rep, buf.getvalue()

    def test_noindex_page_with_header_passes(self):
        base = self.serve_site(noindex=True, robots=False)
        rep, out = self.run_check(base + "/s/", None)
        self.assertEqual(rep.errors, 0, out)
        self.assertIn("X-Robots-Tag", out)

    def test_index_mode_flags_noindex_signals(self):
        base = self.serve_site(noindex=True, robots=False)
        rep, out = self.run_check(base + "/s/", True)
        self.assertGreaterEqual(rep.errors, 2, out)  # header + meta both disagree with --index
        self.assertIn("meant to be indexed", out)

    def test_noindex_mode_flags_a_missing_header(self):
        base = self.serve_site(noindex=False, robots=None)
        rep, out = self.run_check(base + "/s/", False)
        self.assertIn("can be indexed", out)
        self.assertGreaterEqual(rep.errors, 1)

    def test_header_vs_meta_disagreement(self):
        base = self.serve_site(noindex=True, robots=None, extra='<meta name="robots" content="index, follow">')
        rep, out = self.run_check(base + "/s/", False)
        self.assertIn("header says noindex but meta robots says", out)

    def test_wrong_size_and_missing_og_image(self):
        base = self.serve_site(noindex=False, robots=True, image=b"not an image")
        rep, out = self.run_check(base + "/s/", True)
        self.assertIn("not a readable image", out)

    # helpers -------------------------------------------------------------
    def serve_site(self, *, noindex, robots, extra="", image=None):
        d = pathlib.Path(tempfile.mkdtemp())
        handler = type("H", (_Handler,), {"noindex": noindex})
        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(handler, directory=str(d)))
        base = f"http://127.0.0.1:{srv.server_port}"
        src = PAGE.replace("</head>", extra + "</head>")
        html, _ = seo.inject(src, url=f"{base}/s/", image_url=f"{base}/s/_ownnow/og.png", indexable=robots)
        (d / "s/_ownnow").mkdir(parents=True)
        (d / "s/index.html").write_text(html)
        if image is None:
            image = seo.og_image("Field notes", "test") if seo.can_render() else b""
        (d / "s/_ownnow/og.png").write_bytes(image)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        return base


class CheckDirTest(unittest.TestCase):
    def test_dir_mode_softens_injectable_tags(self):
        d = pathlib.Path(tempfile.mkdtemp())
        (d / "index.html").write_text("<title>t</title><h1>t</h1>")
        with contextlib.redirect_stdout(io.StringIO()) as buf:
            rep = seo_check.check_dir(d, True)
        self.assertEqual(rep.errors, 0, buf.getvalue())
        self.assertIn("publish.py adds a default", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
