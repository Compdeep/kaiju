#!/usr/bin/env python3
"""
How long a real planner call takes, and what it costs, per model.

The prompt is the one from the live run: the executive's own 51KB system
prompt, its user turn, and the plan tool forced. A toy question measures
nothing useful here — deepseek-v4-flash spent 84 reasoning tokens on an
arithmetic problem and over 8,192 on this exact prompt.

What comes out of it: how many completion tokens a planner call actually needs
per model, so max_tokens can be set per model instead of one global 4,096; and
how long it takes, so an effort budget in seconds can be scaled by the model's
real pace rather than a guess.
"""
import json, os, sys, time
from concurrent.futures import ThreadPoolExecutor
import urllib.request, urllib.error

KEY = json.load(open("/home/sites/makeen/kaiju.config.json"))["providers"]["openrouter"]["api_key"]
URL = "https://openrouter.ai/api/v1/chat/completions"

P = json.load(open("prompt.json"))
TOOL = json.load(open("plan_tool.json"))

# Generous but bounded. A model that reaches this was not going to stop, which
# is itself the reading — and it caps what one runaway costs.
MAX_TOKENS = 32000
TIMEOUT = 900


def one(model):
    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": P["system"]},
            {"role": "user", "content": P["user"]},
        ],
        "tools": [TOOL],
        "tool_choice": {"type": "function", "function": {"name": "plan"}},
        "temperature": 0.1,
        "max_tokens": MAX_TOKENS,
        "usage": {"include": True},
    }
    req = urllib.request.Request(URL, data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"})
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            d = json.loads(r.read())
    except urllib.error.HTTPError as e:
        return {"model": model, "err": "HTTP %s %s" % (e.code, e.read()[:120].decode("utf8", "replace"))}
    except Exception as e:
        return {"model": model, "err": str(e)[:120]}
    secs = time.time() - t0

    if "choices" not in d or not d["choices"]:
        return {"model": model, "err": "no choices: " + json.dumps(d)[:120], "secs": round(secs, 1)}
    ch = d["choices"][0]
    u = d.get("usage") or {}
    det = u.get("completion_tokens_details") or {}
    calls = ch["message"].get("tool_calls") or []
    steps = None
    if calls:
        try:
            steps = len(json.loads(calls[0]["function"]["arguments"]).get("steps", []))
        except Exception:
            steps = -1
    return {
        "model": model,
        "secs": round(secs, 1),
        "finish": ch.get("finish_reason"),
        "in": u.get("prompt_tokens"),
        "out": u.get("completion_tokens"),
        "reasoning": det.get("reasoning_tokens") or 0,
        "planned": steps,
        "cost": u.get("cost") or 0.0,
    }


models = sys.argv[1:]
results = []
with ThreadPoolExecutor(max_workers=5) as pool:
    for r in pool.map(one, models):
        results.append(r)
        if "err" in r:
            print("%-42s ERR %s" % (r["model"], r["err"][:70]), flush=True)
        else:
            print("%-42s %6.1fs  out=%-6s think=%-6s steps=%-4s %-8s $%.4f" % (
                r["model"], r["secs"], r["out"], r["reasoning"],
                r["planned"], r["finish"], r["cost"]), flush=True)
json.dump(results, open("results.json", "w"), indent=1)
print("\ntotal $%.4f" % sum(r.get("cost", 0) for r in results))
