#!/usr/bin/env python3
"""
Does a model act on `reasoning.effort`, and does it honour `reasoning.max_tokens`?

Every provider accepts both parameters and none errors on either, so asking the
provider tells you nothing. This asks the model: the same prompt at each effort
the catalogue claims it supports, and the reasoning tokens it actually spent.

Reads models/models.json for the models kaiju offers, and OpenRouter's own
catalogue for the efforts each one claims. Writes effort_bench_results.json.
"""
import json, os, statistics, sys, time
from concurrent.futures import ThreadPoolExecutor
import urllib.request, urllib.error

KEY = json.load(open("/home/sites/makeen/kaiju.config.json"))["providers"]["openrouter"]["api_key"]
URL = "https://openrouter.ai/api/v1/chat/completions"

# Bounded on purpose: enough steps that a model has something to think about,
# small enough that even "max" effort lands in hundreds of tokens rather than
# thousands. A prompt that invites open-ended reasoning makes the spend depend
# on the prompt as much as on the setting.
PROMPT = (
    "A shop sells pens at 3 for £4 and notebooks at 2 for £7. "
    "Ann buys 9 pens and 4 notebooks; Ben buys 6 pens and 6 notebooks. "
    "Who spends more, and by how much? Answer in one sentence."
)

RUNS = int(os.environ.get("RUNS", "3"))  # per effort, because reasoning length varies run to run
BASELINE_RUNS = 1 # saying nothing, to see where the model's own default sits
MAX_TOKENS = 16000
TIMEOUT = 300


def call(model, reasoning):
    """One completion. Returns (reasoning_tokens, completion_tokens, finish, cost, error)."""
    body = {
        "model": model,
        "messages": [{"role": "user", "content": PROMPT}],
        "temperature": 0,
        "max_tokens": MAX_TOKENS,
        "usage": {"include": True},
    }
    if reasoning is not None:
        body["reasoning"] = reasoning
    req = urllib.request.Request(
        URL,
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            d = json.loads(r.read())
    except urllib.error.HTTPError as e:
        return None, None, None, 0.0, "HTTP %s %s" % (e.code, e.read()[:200].decode("utf8", "replace"))
    except Exception as e:
        return None, None, None, 0.0, str(e)[:200]
    if "choices" not in d or not d["choices"]:
        return None, None, None, 0.0, "no choices: " + json.dumps(d)[:200]
    u = d.get("usage") or {}
    det = u.get("completion_tokens_details") or {}
    rt = det.get("reasoning_tokens")
    if rt is None:
        # Some providers report no count but return the reasoning text; four
        # characters to the token is close enough to compare settings.
        txt = d["choices"][0]["message"].get("reasoning") or ""
        rt = len(txt) // 4 if txt else 0
    return rt, u.get("completion_tokens"), d["choices"][0].get("finish_reason"), u.get("cost") or 0.0, None


def measure(model, efforts, test_budget):
    out = {"model": model, "efforts": {}, "baseline": [], "budget": {}, "cost": 0.0, "errors": []}
    for _ in range(BASELINE_RUNS):
        rt, ct, fin, cost, err = call(model, None)
        out["cost"] += cost
        if err:
            out["errors"].append("baseline: " + err)
        else:
            out["baseline"].append({"reasoning": rt, "completion": ct, "finish": fin})
    for e in efforts:
        rows = []
        for _ in range(RUNS):
            rt, ct, fin, cost, err = call(model, {"effort": e})
            out["cost"] += cost
            if err:
                out["errors"].append("%s: %s" % (e, err))
                break
            rows.append({"reasoning": rt, "completion": ct, "finish": fin})
        out["efforts"][e] = rows
    if test_budget:
        for want in (256, 2048):
            rows = []
            for _ in range(2):
                rt, ct, fin, cost, err = call(model, {"max_tokens": want})
                out["cost"] += cost
                if err:
                    out["errors"].append("budget %d: %s" % (want, err))
                    break
                rows.append({"reasoning": rt, "completion": ct, "finish": fin})
            out["budget"][str(want)] = rows
    return out


def main():
    orm = {m["id"]: m for m in json.load(open("or_models.json"))["data"]}
    cat = json.load(open("/home/sites/kaiju/kaiju/models/models.json"))["models"]
    jobs = []
    for m in cat:
        if not (m.get("thinking") or m.get("reasoning_optional")):
            continue
        rr = (orm.get(m["id"]) or {}).get("reasoning") or {}
        efforts = [e for e in (rr.get("supported_efforts") or []) if e != "none"]
        budget = bool(rr.get("supports_max_tokens")) or m.get("reasoning_budget")
        if not efforts and not budget:
            continue
        jobs.append((m["id"], efforts, budget))

    only = sys.argv[1:]
    if only:
        jobs = [j for j in jobs if j[0] in only]
    print("measuring %d models" % len(jobs), flush=True)

    results = []
    started = time.time()
    with ThreadPoolExecutor(max_workers=6) as pool:
        for r in pool.map(lambda j: measure(*j), jobs):
            results.append(r)
            spent = sum(x["cost"] for x in results)
            print("  %-42s $%.4f  %s" % (r["model"], r["cost"], "ERR" if r["errors"] else "ok"), flush=True)
    print("done in %ds, $%.4f" % (time.time() - started, sum(r["cost"] for r in results)))
    json.dump(results, open(os.environ.get("OUT", "effort_bench_results.json"), "w"), indent=1)


if __name__ == "__main__":
    main()
