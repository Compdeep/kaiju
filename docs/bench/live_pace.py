#!/usr/bin/env python3
"""
How long each model's calls actually take on a running deployment.

The planner bench beside this one measures one prompt three times. That answers
"how fast is this model on this prompt" and not "how often does this model pass
the deadline", which is the question a per-model allowance is set from — a model
whose median is ordinary and whose tail runs long is cut off just as surely as a
uniformly slow one, and only the tail is cut.

So this reads the prompt debug log the daemon already writes, one file per run,
and reports per model: how many calls, the median, the 90th percentile, the
longest, and how many passed the round deadline.

    python3 live_pace.py [/tmp/kaiju-prompts]

Every stage counts, not only planning: the deadline applies per call, and the
chat lane has one too.
"""
import collections, glob, os, re, sys

LOGS = sys.argv[1] if len(sys.argv) > 1 else "/tmp/kaiju-prompts"

# The default round deadline, agent/round_budget.go. A call within a few seconds
# of it was cut off by it rather than by anything the model chose.
DEADLINE_MS = 120_000
NEAR = 5_000

ENTRY = re.compile(r"^=== (\S+) (\S+) (\S+) (\S+) (\S+) ===(.*?)(?=^=== |\Z)", re.S | re.M)
LATENCY = re.compile(r"latency_ms: (\d+)")
# Stub models from the test suite share the directory and are not measurements.
NOT_A_MODEL = {"stub", "test", "test-model", "none"}


def rows():
    by_model = collections.defaultdict(list)
    for path in glob.glob(os.path.join(LOGS, "*.log")):
        try:
            text = open(path, errors="ignore").read()
        except OSError:
            continue
        for _node, _type, tag, model, _ts, body in ENTRY.findall(text):
            if model in NOT_A_MODEL:
                continue
            found = LATENCY.search(body)
            if found:
                by_model[model].append((tag, int(found.group(1))))
    return by_model


def main():
    by_model = rows()
    if not by_model:
        print(f"no model calls found in {LOGS}")
        return
    print(f"{'model':<38} {'calls':>6} {'median':>8} {'p90':>8} {'longest':>9} {'cut off':>8}")
    for model, calls in sorted(by_model.items(), key=lambda kv: -len(kv[1])):
        lat = sorted(ms for _tag, ms in calls)
        n = len(lat)
        cut = sum(1 for ms in lat if ms >= DEADLINE_MS - NEAR)
        print("%-38s %6d %7.1fs %7.1fs %8.1fs %5d/%d" % (
            model, n, lat[n // 2] / 1000, lat[int(n * 0.9)] / 1000, lat[-1] / 1000, cut, n))


if __name__ == "__main__":
    main()
