#!/usr/bin/env python3
"""
Turns the raw runs into a verdict per model, and says why.

The first pass judged on the ratio between the largest and smallest median
alone, which was wrong twice over. A model that spends NOTHING at its weakest
setting — claude-fable-5.1 reasons not at all below xhigh — divides by zero and
was read as no reading at all, when it is the strongest response in the set.
And a ratio has no direction: deepseek-v4-flash-0731 reasons THREE TIMES LONGER
at "low" than at "high", reproducibly, which is a control that would do the
opposite of what its label says.
"""
import json, os, statistics, sys

ORDER = ["minimal", "low", "medium", "high", "xhigh", "max"]

# Two settings count as separated when their runs do not overlap at all. With
# five runs each that is a real gap rather than a lucky pair of draws, and it
# needs no assumption about how reasoning length is distributed — which is just
# as well, since kimi-k3's is a spike at the token cap.
def separated(a, b):
    return max(a) < min(b)


def load(*paths):
    out = {}
    for p in paths:
        if not os.path.exists(p):
            continue
        for r in json.load(open(p)):
            out[r["model"]] = r  # a later file supersedes an earlier one
    return out


def verdict(r):
    """
    Which of a model's effort values are worth offering, and why.

    A value earns its place by being distinguishable from the weakest one: its
    runs must not overlap that setting's at all. Two labels that produce the
    same thinking are one control with two names — claude-fable-5.1 reasons for
    zero tokens at "low", "medium" AND "high", and only starts at "xhigh", so
    offering the middle three would be three ways to ask for nothing.

    Judged against the weakest setting rather than the next one up, because the
    steps are not evenly spaced and adjacent pairs often overlap where the ends
    do not.
    """
    got = {e: [x["reasoning"] for x in rows] for e, rows in r["efforts"].items() if rows}
    scale = [e for e in ORDER if e in got]
    if len(scale) < 2:
        return "no efforts to compare", [], None
    weakest = scale[0]
    base = got[weakest]

    # A value below the weakest setting means the labels run backwards. Two
    # deepseek models reason three to five times LONGER at "low" than at "high",
    # reproducibly — a control that would do the opposite of what it says.
    if any(separated(got[e], base) for e in scale[1:]):
        return "INVERTED — a stronger setting thinks less", [], None

    keep = [e for e in scale[1:] if separated(base, got[e])]
    if not keep:
        return "no separation — inside the run-to-run spread", [], None

    mlo = statistics.median(base)
    mhi = statistics.median(got[keep[-1]])
    if mhi < mlo * 1.25 and mhi - mlo < 50:
        return "separated but too small to be worth a control", [], (mlo, mhi)
    dropped = [e for e in scale[1:] if e not in keep]
    why = "ACTS ON IT"
    if dropped:
        why += " (dropped %s: not distinguishable from %s)" % (",".join(dropped), weakest)
    return why, [weakest] + keep, (mlo, mhi)


def main():
    res = load("effort_bench_results.json", "effort_bench_pass2.json")
    acts, no, inv = [], [], []
    for m in sorted(res):
        r = res[m]
        v, scale, ends = verdict(r)
        cells = "  ".join(
            "%s=%s" % (e, int(statistics.median([x["reasoning"] for x in r["efforts"][e]])))
            for e in scale
        )
        line = "%-42s %-46s %s" % (m, cells, v)
        (acts if v.startswith("ACTS") else inv if v.startswith("INVERTED") else no).append((m, scale, line))

    print("ACTS ON EFFORT (%d) — record these\n" % len(acts))
    for _, _, l in acts:
        print("  " + l)
    print("\nDOES NOT (%d) — record nothing\n" % len(no))
    for _, _, l in no:
        print("  " + l)
    print("\nINVERTED (%d) — record nothing; the control would read backwards\n" % len(inv))
    for _, _, l in inv:
        print("  " + l)

    print("\n\nCATALOG PATCH")
    for m, scale, _ in acts:
        print('  %-42s reasoning_efforts = %s' % (m, json.dumps(scale)))


if __name__ == "__main__":
    main()
