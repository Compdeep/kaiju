"""kaiju reference plugin host.

A small FastAPI service that loads the plugins in this directory and exposes them
over the kaiju remote-plugin protocol:

  GET  /plugins        -> the manifest (every plugin, its tools + JSON schemas + skill)
  POST /invoke/{tool}  -> run a tool, return a kaiju ToolMessage envelope
  GET  /health         -> liveness + the loaded plugin names

Plugins are LOADED ONCE at startup and stay warm — this process is a long-running
service, not spawned per request — so a plugin can hold warm state (a Playwright
browser pool, a loaded model, a connection pool) across many calls.

Run:
  pip install -r requirements.txt
  uvicorn host:app --host 127.0.0.1 --port 8091

Optional auth: set KAIJU_PLUGIN_TOKEN and every request must send
`Authorization: Bearer <token>` (kaiju's bridge does this via KAIJU_PLUGIN_TOKEN).
"""
from __future__ import annotations

import os

from fastapi import FastAPI, Header, HTTPException
from pydantic import BaseModel

from registry import Registry, load_plugins

app = FastAPI(title="kaiju plugin host")

# Loaded once, at startup — plugins stay warm for the life of the process.
REGISTRY: Registry = load_plugins(os.path.dirname(os.path.abspath(__file__)))

_TOKEN = os.environ.get("KAIJU_PLUGIN_TOKEN", "")


def _auth(authorization: str | None) -> None:
    """Enforce the shared secret when one is configured; a no-op otherwise."""
    if not _TOKEN:
        return
    if authorization != f"Bearer {_TOKEN}":
        raise HTTPException(status_code=401, detail="missing or invalid authorization")


class InvokeBody(BaseModel):
    params: dict = {}


@app.get("/health")
def health() -> dict:
    return {"ok": True, "plugins": [p["name"] for p in REGISTRY.manifest()["plugins"]]}


@app.get("/plugins")
def plugins(authorization: str | None = Header(default=None)) -> dict:
    _auth(authorization)
    return REGISTRY.manifest()


@app.post("/invoke/{tool}")
async def invoke(tool: str, body: InvokeBody, authorization: str | None = Header(default=None)) -> dict:
    _auth(authorization)
    if not REGISTRY.has_tool(tool):
        raise HTTPException(status_code=404, detail=f"unknown tool: {tool}")
    return await REGISTRY.invoke(tool, body.params)
