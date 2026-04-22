import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from main import transform

cases = [("hello", "HELLO"), ("World", "WORLD"), ("abc123", "ABC123")]
for inp, want in cases:
    got = transform(inp)
    assert got == want, f"transform({inp!r}) = {got!r}, want {want!r}"

print("test_main ok")
