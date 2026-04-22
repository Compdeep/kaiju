from target import f

result = f()
assert isinstance(result, int), f"f() must return int, got {type(result).__name__}"
assert result == 2, f"test_b: expected 2, got {result!r}"
print("test_b ok")
