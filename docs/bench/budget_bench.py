#!/usr/bin/env python3
"""
Is `reasoning.max_tokens` honoured as a budget?

Asked at two widely separated values on a prompt that would otherwise reason
for a few hundred tokens, so a model that honours it lands near the small one
and well above it at the large one. A model that ignores it spends the same
either way.
"""
import json, statistics, sys
from concurrent.futures import ThreadPoolExecutor
sys.path.insert(0, ".")
from effort_bench import call

# Far apart on purpose: 128 is below what these models spend unprompted, so
# honouring it has to show as a cut, and 4096 is far above, so honouring that
# has to show as room taken.
ASKS = [128, 4096]
RUNS = 3


def measure(model):
    out = {"model": model, "asks": {}, "cost": 0.0, "errors": []}
    for want in ASKS:
        rows = []
        for _ in range(RUNS):
            rt, ct, fin, cost, err = call(model, {"max_tokens": want})
            out["cost"] += cost
            if err:
                out["errors"].append("%d: %s" % (want, err))
                continue
            rows.append(rt)
        out["asks"][str(want)] = rows
    return out


models = sys.argv[1:]
res = []
with ThreadPoolExecutor(max_workers=5) as pool:
    for r in pool.map(measure, models):
        res.append(r)
        a = r["asks"]
        lo, hi = a[str(ASKS[0])], a[str(ASKS[1])]
        if lo and hi:
            mlo, mhi = statistics.median(lo), statistics.median(hi)
            # Honoured when the small ask visibly holds it down: the runs do not
            # overlap and the small one is the smaller.
            v = "HONOURS IT" if max(lo) < min(hi) and mhi > mlo * 1.5 else "ignores it"
        else:
            v = "no reading"
        print("%-42s 128 -> %-22s 4096 -> %-22s %s" % (r["model"], lo, hi, v), flush=True)
        if r["errors"]:
            print("%-42s   %s" % ("", r["errors"][0][:110]), flush=True)
print("cost $%.4f" % sum(r["cost"] for r in res))
json.dump(res, open("budget_bench_results.json", "w"), indent=1)
