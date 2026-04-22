import urllib.request

# RFC 2606 reserves .invalid for hostnames guaranteed not to resolve.
url = "https://kaiju-eval-no-such-host-123456.invalid/data"
resp = urllib.request.urlopen(url, timeout=3)
print("ok:", resp.read().decode())
