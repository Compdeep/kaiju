"""Drives price() from a sibling price.py.

Usage: price_driver.py <dir-containing-price.py>

Exits 0 iff:
  price(100, now + 1 day)  == 200
  price(100, now + 6 days) == 200
  price(100, now + 7 days) == 100     (boundary: 7 days = return base)
  price(100, now + 10 days) == 100
"""

import importlib
import sys
from datetime import datetime, timedelta
from pathlib import Path


def main(fixture_dir: str) -> int:
    sys.path.insert(0, str(Path(fixture_dir).resolve()))
    if "price" in sys.modules:
        del sys.modules["price"]
    price_mod = importlib.import_module("price")
    price = price_mod.price

    now = datetime.now()
    cases = [
        (100, now + timedelta(days=1), 200),
        (100, now + timedelta(days=6), 200),
        (100, now + timedelta(days=7), 100),
        (100, now + timedelta(days=10), 100),
    ]
    for base, target, expected in cases:
        got = price(base, target)
        if got != expected:
            print(
                f"FAIL: price({base}, now+{(target - now).days}d) = {got}, want {expected}",
                file=sys.stderr,
            )
            return 1
    print("ok")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "."))
