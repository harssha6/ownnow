#!/usr/bin/env python3
"""Read a publish's built-in stats using the installed skill's server/key settings."""
import argparse
import json
import urllib.parse

from publish import api_key, call


def days(value):
    try:
        count = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError("days must be a number from 1 to 365")
    if not 1 <= count <= 365:
        raise argparse.ArgumentTypeError("days must be from 1 to 365")
    return count


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Read totals, daily counts, referrer hosts and paths as JSON. "
                    "Daily buckets use UTC; period uniques sum daily uniques.",
        epilog="Uses OWNNOW_SERVER / OWNNOW_API_KEY or local/config.json. "
               "OWNNOW_CLAIM supplies a site claim token. Credentials are required.")
    parser.add_argument("slug", help="publish slug")
    parser.add_argument("--days", type=days, default=7, help="days including today (1–365, default 7)")
    args = parser.parse_args(argv)
    slug = urllib.parse.quote(args.slug, safe="")
    result = call("GET", f"/api/v1/publishes/{slug}/stats?days={args.days}", api_key())
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
