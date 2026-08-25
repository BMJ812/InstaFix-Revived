# Telegram Reel previews: current design and the 20 MiB boundary

Last verified: 2026-08-25.

## Summary

InstaFix Revived now treats Telegram Reel previews as a size-constrained delivery problem rather than a generic OpenGraph problem.

Fresh Telegram WebPage ingestion was measured with otherwise-identical valid MP4 files. The boundary was exact:

- `20,971,520` bytes (`20 MiB`) -> Telegram attached a video document.
- `20,971,521` bytes -> Telegram did not attach the video document and the preview fell back to the image/poster.

The runtime therefore clamps `MAX_INLINE_VIDEO_BYTES` to at most `20,971,520` bytes. Values above that are intentionally ignored for Telegram-safe preview decisions.

## Current Reel flow

For a normal Telegram Reel request:

1. Resolve the public Instagram metadata and the progressive video URL.
2. Read the original media Content-Length when available.
3. If the original is at or below 20 MiB, advertise the normal same-origin offload URL. The media can remain unmodified.
4. If the original is above 20 MiB, advertise `compact=av4` instead of relying on an oversized original.
5. The compact path is fast-first:
   - an inexpensive DASH video+audio remux can be served immediately when available;
   - a better smart transcode is built in the background and cached;
   - the smart encoder targets `20,471,520` bytes, leaving about 500 KB of safety headroom below Telegram's measured hard boundary;
   - resolution is kept as high as practical (normally 720p, then lower fallbacks if required).

The compact media cache is disposable runtime state. The normal stateless path still avoids turning the application server into a general Instagram media proxy.

## Why this was confusing

Several plausible explanations were tested and ruled out for oversized fresh previews, including:

- OpenGraph tag order and metadata differences;
- truthful versus stale video dimensions;
- direct Instagram CDN URLs versus same-origin offload URLs;
- HTTP 302 redirects;
- HTTP 200 streaming from the application;
- Cloudflare Worker media proxying;
- page-generation latency and edge caching;
- switching the media/page hostname.

Telegram was observed downloading a full oversized MP4 and still declining to attach it as the WebPage video document. The decisive variable was size.

An older Reel could appear to work above 20 MiB through another embed service because Telegram already had a historical cached document for that URL/media. Fresh large Reels tested through that service were subject to the same practical boundary. Do not use an old cached preview as evidence that a fresh >20 MiB WebPage video will be accepted today.

## Cache-busting note

During testing, changing only the query string on an `og:video` URL was not a reliable way to force Telegram to ingest a new media object. Telegram could reuse a previously cached media document.

For strict A/B testing, use a genuinely unique media path rather than relying only on `?v=...`.

## Monitoring

The optional daily Telegram report keeps a bounded per-Reel decision journal. It distinguishes:

- `direct` — original video is at/below 20 MiB;
- `compact` — original is above 20 MiB and the compact path is advertised;
- `expected image` — a video document is not expected to succeed, for example when an oversized Reel has no usable compact source or a Reel resolves as an image;
- `blocked` — retrieval/restriction failures such as age restriction, region restriction, private/deleted/not-found media, expired media URLs, rate limiting, timeouts, or upstream 5xx errors.

When a Telegram media GET reaches the application, the journal also records whether the compact file served was `smart` or `dash` and its actual byte size.

This is server-side decision/fetch telemetry. It does not pretend that the application can see the final Telegram client UI. CDN/edge caches may also satisfy some media requests without a request reaching the application.

## Configuration

Recommended current settings:

```env
INSTAFIX_EXPERIMENT_MODE=stateless_cloudrun
INSTAFIX_PUBLIC_VIDEO_REFRESH_DIRECT=true
MAX_INLINE_VIDEO_BYTES=20971520
INSTAFIX_STATELESS_EDGE_TTL_SECONDS=300
INSTAFIX_STATELESS_CDN_EXPIRY_MARGIN_SECONDS=1800
```

Optional operational report:

```env
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHAT_ID=
DAILY_REPORT_HOUR_UTC=0
REPORT_ADMIN_TOKEN=
```

Never commit real bot tokens, admin tokens, or Instagram cookies.

## Cloudflare Worker

`deploy/cloudflare-worker/` contains the edge layer used by the public instance. The checked-in configuration is tailored to `instagram7.com`; self-hosters should change the Worker name, routes, zone and `ORIGIN` for their own domain.

The 20 MiB rule is not a Cloudflare limit. The Worker is useful for routing/caching, but the fresh Telegram WebPage media boundary was reproduced independently of redirect/proxy delivery details.
