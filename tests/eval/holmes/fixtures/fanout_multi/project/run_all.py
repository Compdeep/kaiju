import sys
from services import a, b, c

errors = []
for name, mod in [("a", a), ("b", b), ("c", c)]:
    try:
        mod.run()
    except Exception as e:
        errors.append(f"{name}: {type(e).__name__}: {e}")

if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)

print("ok")
