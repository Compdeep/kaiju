"""webreader — read a web page as clean text, rendering JavaScript only when a
fast static fetch comes back thin.

Two tiers, cheap first:

  Tier 1  trafilatura downloads + extracts the main article text. No browser.
  Tier 2  (only if tier 1 is thin, or render=true, AND Playwright is installed)
          render the page in a WARM headless browser, then extract with
          trafilatura.

The Playwright browser is launched ONCE and reused across calls — the host is a
long-running service — so rendering pays a browser launch only at startup, not
per fetch. If Playwright isn't installed, tier 2 is skipped and tier 1 stands on
its own (a real improvement over a plain fetch by itself).
"""
from __future__ import annotations

import asyncio

try:
    import trafilatura
except Exception:  # dependency not installed yet
    trafilatura = None

MANIFEST = {
    "name": "webreader",
    "description": "Read web pages as clean text, rendering JavaScript when needed.",
    # Serve up to this many reads at once (a DAG wave fans out several parallel
    # web_fetches). Static reads offload to a threadpool and renders are async, so
    # these run truly concurrently; the cap bounds memory (each render is a browser
    # context). Requests beyond it queue for a slot (kaiju's 60s call timeout caps
    # the wait, then it falls back to the built-in reader).
    "max_concurrency": 4,
    "tools": [
        {
            "name": "web_read",
            "description": (
                "Fetch a URL and return the main readable text of the page, with "
                "boilerplate (nav, ads, cookie banners) removed. Renders "
                "JavaScript-heavy pages when a plain fetch yields nothing. Use for "
                "articles, reports, documentation, and primary sources a search "
                "turned up."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "url": {"type": "string", "description": "The URL to read."},
                    "render": {
                        "type": "boolean",
                        "description": "Force JS rendering even if a static fetch succeeds (default: render only when the static read is thin).",
                    },
                },
                "required": ["url"],
                "additionalProperties": False,
            },
            "impact": "observe",
            # reader: kaiju wires this as web_fetch's ReaderFallback, so once the
            # plugin is enabled every fetch reads through webreader automatically
            # (rendering JS pages), not only when the planner calls web_read.
            "reader": True,
        }
    ],
}

# Below this many characters the static read is 'thin' and we escalate to render.
_MIN_CHARS = 400


def _ok(text: str, url: str, via: str) -> dict:
    return {"kind": "page", "status": "ok", "content": text,
            "data": {"url": url, "chars": len(text), "via": via}}


def _empty(detail: str, url: str) -> dict:
    return {"kind": "page", "status": "empty", "detail": detail, "data": {"url": url}}


def _error(detail: str, url: str) -> dict:
    return {"kind": "page", "status": "error", "detail": detail, "data": {"url": url}}


def _extract(html_or_none: str | None) -> str:
    if trafilatura is None or not html_or_none:
        return ""
    return (trafilatura.extract(html_or_none, include_comments=False, include_tables=True) or "").strip()


def _static_sync(url: str) -> str:
    if trafilatura is None:
        return ""
    return _extract(trafilatura.fetch_url(url))


async def _read_static(url: str) -> str:
    # trafilatura is blocking (network + lxml); keep it off the event loop.
    loop = asyncio.get_event_loop()
    return await loop.run_in_executor(None, _static_sync, url)


# --- Tier 2: Playwright render, lazy + warm ---------------------------------
_browser = None
_playwright = None
_browser_lock = asyncio.Lock()


async def _get_browser():
    """Return one warm headless browser, launching it on first use. Returns None
    when the render tier is unavailable — Playwright not installed, OR its browser
    binary not downloaded (`playwright install chromium`) — so webreader falls
    back to tier 1 gracefully instead of erroring."""
    global _browser, _playwright
    if _browser is not None:
        return _browser
    async with _browser_lock:
        if _browser is None:
            try:
                from playwright.async_api import async_playwright

                _playwright = await async_playwright().start()
                _browser = await _playwright.chromium.launch(headless=True)
            except Exception:
                _playwright = None
                _browser = None
                return None
    return _browser


async def _read_rendered(url: str) -> str:
    browser = await _get_browser()
    if browser is None:
        return ""
    page = await browser.new_page()
    try:
        await page.goto(url, wait_until="networkidle", timeout=20000)
        html = await page.content()
    finally:
        await page.close()
    loop = asyncio.get_event_loop()
    return await loop.run_in_executor(None, _extract, html)


async def invoke(tool: str, params: dict) -> dict:
    url = str((params or {}).get("url", "")).strip()
    if not url:
        return _error("'url' is required", "")
    if trafilatura is None:
        return _error("webreader dependencies not installed (pip install -r requirements.txt)", url)
    force_render = bool((params or {}).get("render"))

    if not force_render:
        text = await _read_static(url)
        if len(text) >= _MIN_CHARS:
            return _ok(text, url, "static")

    # Static was thin (or render forced) — escalate to a rendered read.
    text = await _read_rendered(url)
    if len(text) >= _MIN_CHARS:
        return _ok(text, url, "rendered")

    # Both tiers thin: report the gap honestly (kaiju's coverage edge reads this
    # 'empty' status rather than letting the answer fabricate).
    return _empty("no extractable content (login-walled, image-only, or blocked)", url)
