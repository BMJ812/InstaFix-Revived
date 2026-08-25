#!/usr/bin/env python3
"""Structural A/B checker for the stateless InstaFix Azure experiment.

This does not pretend to emulate Telegram's final preview classifier. It checks
what we can prove over HTTP before manual Telegram A/B:
- Azure experiment handler served the page;
- og:video exists and is a direct HTTPS Instagram CDN URL;
- no Instagram7 /offload or /videos MP4 is advertised;
- signed CDN URL is not already expired/near expiry;
- the advertised CDN object is usable via HEAD or a one-byte Range GET;
- optional legacy-origin metadata for side-by-side diagnostics.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from dataclasses import asdict, dataclass
from html.parser import HTMLParser
from pathlib import Path
from typing import Optional
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlparse
from urllib.request import Request, urlopen

TELEGRAM_UA = "TelegramBot (like TwitterBot)"
CDN_SUFFIXES = ("cdninstagram.com", "fbcdn.net")
EXPECTED_EXPERIMENT = "stateless-azure"


class MetaParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.meta: dict[str, str] = {}

    def handle_starttag(self, tag: str, attrs: list[tuple[str, Optional[str]]]) -> None:
        if tag.lower() != "meta":
            return
        values = {k.lower(): (v or "") for k, v in attrs}
        key = values.get("property") or values.get("name")
        content = values.get("content")
        if key and content and key not in self.meta:
            self.meta[key] = content


@dataclass
class MediaProbe:
    method: str = ""
    status: Optional[int] = None
    content_type: str = ""
    accept_ranges: str = ""
    usable: bool = False
    error: str = ""


@dataclass
class Result:
    shortcode: str
    label: str
    status: int
    experiment: str
    og_video: str
    og_image: str
    video_host: str
    direct_cdn: bool
    local_proxy_advertised: bool
    signed_expiry_unix: Optional[int]
    signed_seconds_left: Optional[int]
    cdn_probe_method: str
    cdn_probe_status: Optional[int]
    cdn_probe_content_type: str
    cdn_probe_accept_ranges: str
    legacy_og_video: str
    ok: bool
    errors: list[str]


def request_text(url: str) -> tuple[int, dict[str, str], str]:
    req = Request(url, headers={"User-Agent": TELEGRAM_UA, "Accept": "text/html,*/*"})
    try:
        with urlopen(req, timeout=25) as res:
            body = res.read(2 * 1024 * 1024 + 1)
            if len(body) > 2 * 1024 * 1024:
                raise RuntimeError("HTML response exceeded 2 MiB")
            headers = {k.lower(): v for k, v in res.headers.items()}
            return res.status, headers, body.decode("utf-8", errors="replace")
    except HTTPError as exc:
        body = exc.read(256 * 1024).decode("utf-8", errors="replace")
        headers = {k.lower(): v for k, v in exc.headers.items()}
        return exc.code, headers, body


def parse_meta(html: str) -> dict[str, str]:
    parser = MetaParser()
    parser.feed(html)
    return parser.meta


def is_instagram_cdn(raw: str) -> bool:
    try:
        parsed = urlparse(raw)
    except ValueError:
        return False
    host = (parsed.hostname or "").lower().rstrip(".")
    return parsed.scheme == "https" and any(host == suffix or host.endswith("." + suffix) for suffix in CDN_SUFFIXES)


def signed_expiry(raw: str) -> Optional[int]:
    try:
        value = parse_qs(urlparse(raw).query).get("oe", [""])[0].strip()
        if not value:
            return None
        result = int(value, 16)
        return result if result > 0 else None
    except (ValueError, OverflowError):
        return None


def usable_media_response(status: Optional[int], content_type: str) -> bool:
    if status not in (200, 206):
        return False
    content_type = content_type.lower().strip()
    if not content_type:
        return True
    if content_type.startswith("text/") or "json" in content_type:
        return False
    return content_type.startswith("video/") or content_type in {
        "application/octet-stream",
        "binary/octet-stream",
    }


def do_media_probe(raw: str, method: str, range_header: str = "") -> MediaProbe:
    headers = {
        "User-Agent": TELEGRAM_UA,
        "Accept": "video/mp4,video/*;q=0.9,*/*;q=0.1",
    }
    if range_header:
        headers["Range"] = range_header
    req = Request(raw, method=method, headers=headers)
    try:
        with urlopen(req, timeout=12) as res:
            if method == "GET":
                # Never download the Reel body during a corpus probe. If the CDN
                # ignores Range and returns 200, closing after one byte still
                # gives us the response classification without origin egress.
                res.read(1)
            content_type = res.headers.get("Content-Type", "")
            accept_ranges = res.headers.get("Accept-Ranges", "")
            return MediaProbe(
                method=method if not range_header else f"{method} range=0-0",
                status=res.status,
                content_type=content_type,
                accept_ranges=accept_ranges,
                usable=usable_media_response(res.status, content_type),
            )
    except HTTPError as exc:
        content_type = exc.headers.get("Content-Type", "") if exc.headers else ""
        accept_ranges = exc.headers.get("Accept-Ranges", "") if exc.headers else ""
        return MediaProbe(
            method=method if not range_header else f"{method} range=0-0",
            status=exc.code,
            content_type=content_type,
            accept_ranges=accept_ranges,
            usable=usable_media_response(exc.code, content_type),
            error=f"HTTP {exc.code}",
        )
    except (URLError, TimeoutError, ValueError) as exc:
        return MediaProbe(
            method=method if not range_header else f"{method} range=0-0",
            error=str(exc),
        )


def probe_media(raw: str) -> MediaProbe:
    if not raw:
        return MediaProbe(error="empty URL")
    head = do_media_probe(raw, "HEAD")
    if head.usable:
        return head
    ranged = do_media_probe(raw, "GET", "bytes=0-0")
    if ranged.usable:
        return ranged
    if ranged.status is not None or ranged.error:
        return ranged
    return head


def fetch_legacy_video(base: str, shortcode: str) -> str:
    if not base:
        return ""
    try:
        _, _, html = request_text(base.rstrip("/") + "/reel/" + shortcode + "/")
        return parse_meta(html).get("og:video", "")
    except Exception:
        return ""


def empty_result(shortcode: str, label: str, legacy_base: str, error: str) -> Result:
    return Result(
        shortcode=shortcode,
        label=label,
        status=0,
        experiment="",
        og_video="",
        og_image="",
        video_host="",
        direct_cdn=False,
        local_proxy_advertised=False,
        signed_expiry_unix=None,
        signed_seconds_left=None,
        cdn_probe_method="",
        cdn_probe_status=None,
        cdn_probe_content_type="",
        cdn_probe_accept_ranges="",
        legacy_og_video=fetch_legacy_video(legacy_base, shortcode),
        ok=False,
        errors=[error],
    )


def check_one(base: str, legacy_base: str, shortcode: str, label: str, min_expiry_seconds: int) -> Result:
    errors: list[str] = []
    page_url = base.rstrip("/") + "/reel/" + shortcode + "/"
    try:
        status, headers, html = request_text(page_url)
    except Exception as exc:
        return empty_result(shortcode, label, legacy_base, f"request failed: {exc}")

    meta = parse_meta(html)
    video = meta.get("og:video", "")
    image = meta.get("og:image", "")
    experiment = headers.get("x-instagram7-experiment", "")
    parsed = urlparse(video) if video else None
    host = (parsed.hostname or "") if parsed else ""
    direct = is_instagram_cdn(video)
    lowered = video.lower()
    local_proxy = "instagram7" in host.lower() and ("/offload/" in lowered or "/videos/" in lowered)
    expiry = signed_expiry(video)
    seconds_left = None if expiry is None else expiry - int(time.time())
    probe = probe_media(video) if direct else MediaProbe()

    if status != 200:
        errors.append(f"HTML status {status}, expected 200")
    if experiment != EXPECTED_EXPERIMENT:
        errors.append(f"wrong experiment header: got {experiment!r}, expected {EXPECTED_EXPERIMENT!r}")
    if not video:
        errors.append("missing og:video")
    elif not direct:
        errors.append(f"og:video is not direct Instagram CDN: {video}")
    if local_proxy:
        errors.append("local MP4 proxy is still advertised")
    if seconds_left is not None and seconds_left < min_expiry_seconds:
        errors.append(f"signed CDN URL expires too soon: {seconds_left}s")
    if direct and not probe.usable:
        detail = probe.error or f"HTTP {probe.status}"
        errors.append(
            f"CDN media probe failed via {probe.method or 'HEAD/Range GET'}: {detail}; "
            f"content-type={probe.content_type!r}"
        )

    return Result(
        shortcode=shortcode,
        label=label,
        status=status,
        experiment=experiment,
        og_video=video,
        og_image=image,
        video_host=host,
        direct_cdn=direct,
        local_proxy_advertised=local_proxy,
        signed_expiry_unix=expiry,
        signed_seconds_left=seconds_left,
        cdn_probe_method=probe.method,
        cdn_probe_status=probe.status,
        cdn_probe_content_type=probe.content_type,
        cdn_probe_accept_ranges=probe.accept_ranges,
        legacy_og_video=fetch_legacy_video(legacy_base, shortcode),
        ok=not errors,
        errors=errors,
    )


def read_corpus(path: Path) -> list[tuple[str, str]]:
    rows: list[tuple[str, str]] = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split(None, 1)
        shortcode = parts[0].strip().strip("/")
        label = parts[1].strip() if len(parts) == 2 else ""
        if shortcode:
            rows.append((shortcode, label))
    return rows


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", required=True, help="Azure stateless origin, e.g. https://<app>.<region>.azurecontainerapps.io")
    parser.add_argument("--legacy-base", default="", help="Optional current production origin for metadata comparison")
    parser.add_argument("--corpus", default="docs/stateless-reel-corpus.txt")
    parser.add_argument("--min-expiry-seconds", type=int, default=600)
    args = parser.parse_args()

    corpus = read_corpus(Path(args.corpus))
    if not corpus:
        print(f"No Reel shortcodes found in {args.corpus}", file=sys.stderr)
        return 2

    results = [check_one(args.base, args.legacy_base, code, label, args.min_expiry_seconds) for code, label in corpus]
    for result in results:
        print(json.dumps(asdict(result), ensure_ascii=False, sort_keys=True))

    passed = sum(1 for result in results if result.ok)
    print(f"summary: {passed}/{len(results)} structural checks passed", file=sys.stderr)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
