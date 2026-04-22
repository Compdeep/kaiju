from target import f

result = f()
assert isinstance(result, int), f"f() must return int, got {type(result).__name__}"
assert result == 1, f"test_a: expected 1, got {result!r}"
print("test_a ok")
