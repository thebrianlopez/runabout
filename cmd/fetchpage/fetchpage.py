#!/usr/bin/env python3
"""fetchpage — fetch fully rendered HTML using CloakBrowser stealth Chromium."""

import argparse
import sys
import time

VERSION = "dev"


def fetch(url: str, timeout_sec: float, wait_sec: float, humanize: bool) -> str:
    try:
        from cloakbrowser import launch
    except ImportError:
        print("fetchpage: cloakbrowser not installed. Run: pip install cloakbrowser", file=sys.stderr)
        sys.exit(1)

    browser = launch(headless=True, humanize=humanize)
    try:
        page = browser.new_page()
        page.goto(url, wait_until="networkidle", timeout=timeout_sec * 1000)
        if wait_sec > 0:
            time.sleep(wait_sec)
        return page.content()
    finally:
        browser.close()


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="fetchpage",
        description="Fetch fully rendered HTML from a URL using CloakBrowser stealth Chromium.",
    )
    subparsers = parser.add_subparsers(dest="subcommand")
    subparsers.add_parser("version", help="Print version information")

    parser.add_argument("url", nargs="?", help="URL to fetch")
    parser.add_argument("--timeout", type=float, default=30, metavar="N",
                        help="navigation timeout in seconds (default: 30)")
    parser.add_argument("--wait", type=float, default=3, metavar="N",
                        help="seconds to wait after page load (default: 3)")
    parser.add_argument("--no-humanize", dest="humanize", action="store_false", default=True,
                        help="disable humanized mouse/keyboard behavior")
    # Kept for backward compatibility — ignored, CloakBrowser is always stealth
    parser.add_argument("--stealth", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--channel", help=argparse.SUPPRESS)

    args = parser.parse_args()

    if args.subcommand == "version":
        print(f"fetchpage {VERSION} (cloakbrowser)", file=sys.stderr)
        return

    if not args.url:
        parser.print_usage(sys.stderr)
        sys.exit(1)

    try:
        content = fetch(args.url, args.timeout, args.wait, args.humanize)
        print(content)
    except Exception as e:
        print(f"fetchpage: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
