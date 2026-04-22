import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
tests = ["test_a.py", "test_b.py"]
failures = []
for t in tests:
    r = subprocess.run(["python3", t], capture_output=True, text=True, cwd=HERE)
    if r.returncode != 0:
        failures.append(f"{t}: {r.stderr.strip()}")

if failures:
    print("\n".join(failures), file=sys.stderr)
    sys.exit(1)

print("all tests passed")
