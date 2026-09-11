"""
A budget only shows as a budget where it BINDS. On the arithmetic prompt these
models reason for about 130 tokens unprompted, so asking for 128 cannot be told
apart from their own default. This asks something that needs real work.
"""
import sys
sys.path.insert(0, ".")
import effort_bench
from concurrent.futures import ThreadPoolExecutor

effort_bench.PROMPT = (
    "Five houses in a row, each a different colour, each with one occupant of a "
    "different nationality, drinking a different drink. The Brit lives in the red "
    "house. The Swede drinks tea. The Dane lives in the green house. The green "
    "house is immediately left of the white house. The person in the centre house "
    "drinks milk. The Norwegian lives in the first house. The yellow house's "
    "occupant drinks coffee. The German lives next to the blue house. "
    "Who lives in the white house? Reason it through, then answer."
)
effort_bench.MAX_TOKENS = 8000
effort_bench.TIMEOUT = 180

MODELS = sys.argv[1:]


def go(m):
    out = [m]
    for want in (None, 256, 4096):
        rows = []
        for _ in range(2):
            rt, ct, fin, cost, err = effort_bench.call(m, None if want is None else {"max_tokens": want})
            rows.append(rt if not err else "ERR:" + err[:30])
        out.append((want, rows))
    return out


with ThreadPoolExecutor(max_workers=3) as p:
    for r in p.map(go, MODELS):
        print(r[0], flush=True)
        for want, rows in r[1:]:
            print("   ask %-8s -> %s" % (want if want else "nothing", rows), flush=True)
