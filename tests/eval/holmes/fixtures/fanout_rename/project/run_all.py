import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
scripts = ["bin/x.py", "bin/y.py", "bin/z.py"]
errors = []
outputs = []
for s in scripts:
    r = subprocess.run(["python3", s], capture_output=True, text=True, cwd=HERE)
    if r.returncode != 0:
        errors.append(f"{s}: {r.stderr.strip()}")
    else:
        outputs.append(r.stdout.strip())

if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)

print("\n".join(outputs))
