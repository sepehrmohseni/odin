#!/usr/bin/env python3
"""Keep running totals of the repository's clones and views, and of the container image's
downloads, and write them as shields.io endpoint badges.

GitHub's traffic API reports only the last 14 days and needs a token with read access to the
repository's administration settings, so a daily workflow (.github/workflows/traffic.yml) runs
this and commits the result to the traffic-data branch:

  traffic.json     per-day counts, merged across runs (the source of the totals)
  clones.json      badge: total clones
  views.json       badge: total views
  image-pulls.json badge: container image downloads (from the public package page)

Usage: TRAFFIC_TOKEN=... REPO=owner/name python3 scripts/traffic-badges.py <data-dir>
"""

import json
import os
import re
import sys
import urllib.request

API = "https://api.github.com"
COLOR = "0b6e7a"


def get(url, token=None):
    req = urllib.request.Request(url, headers={"User-Agent": "odin-traffic-badges"})
    if token:
        req.add_header("Authorization", "Bearer " + token)
        req.add_header("Accept", "application/vnd.github+json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.read().decode()


def short(n):
    if n >= 1_000_000:
        return f"{n / 1_000_000:.1f}M"
    if n >= 10_000:
        return f"{n / 1000:.0f}k"
    if n >= 1000:
        return f"{n / 1000:.1f}k"
    return str(n)


def badge(path, label, message):
    with open(path, "w") as f:
        json.dump({"schemaVersion": 1, "label": label, "message": message, "color": COLOR}, f)
        f.write("\n")


def main():
    out = sys.argv[1]
    repo = os.environ["REPO"]
    token = os.environ.get("TRAFFIC_TOKEN", "")
    history_path = os.path.join(out, "traffic.json")
    try:
        with open(history_path) as f:
            history = json.load(f)
    except FileNotFoundError:
        history = {"clones": {}, "views": {}, "imagePulls": None}

    if token:
        for kind in ("clones", "views"):
            data = json.loads(get(f"{API}/repos/{repo}/traffic/{kind}", token))
            for day in data.get(kind, []):
                # A day's counts only grow while it is within the 14-day window; keep the latest.
                history[kind][day["timestamp"][:10]] = {"count": day["count"], "uniques": day["uniques"]}
    else:
        print("TRAFFIC_TOKEN is not set: clones and views are not updated", file=sys.stderr)

    # The package page is public HTML; its "Total downloads" figure has no API. If the page
    # changes, keep the last known value rather than failing.
    owner, name = repo.split("/")
    try:
        page = get(f"https://github.com/{owner}/{name}/pkgs/container/{name}")
        m = re.search(r'Total downloads</span>\s*<h3[^>]*title="([\d,]+)"', page)
        if m:
            history["imagePulls"] = short(int(m.group(1).replace(",", "")))
    except Exception as e:  # noqa: BLE001 - a scrape failure must not stop the other badges
        print(f"image downloads not updated: {e}", file=sys.stderr)

    with open(history_path, "w") as f:
        json.dump(history, f, indent=1, sort_keys=True)
        f.write("\n")

    clones = sum(d["count"] for d in history["clones"].values())
    views = sum(d["count"] for d in history["views"].values())
    badge(os.path.join(out, "clones.json"), "clones", short(clones) if token or clones else "pending")
    badge(os.path.join(out, "views.json"), "views", short(views) if token or views else "pending")
    badge(os.path.join(out, "image-pulls.json"), "image pulls", history["imagePulls"] or "n/a")
    print(f"clones {clones}, views {views}, image pulls {history['imagePulls']}")


if __name__ == "__main__":
    main()
