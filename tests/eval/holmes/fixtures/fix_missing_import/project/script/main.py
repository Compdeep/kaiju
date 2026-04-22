import sys


def main(argv):
    data = {
        "tool": argv[0],
        "args": argv[1:],
        "count": len(argv) - 1,
    }
    # Bug: json is used but never imported.
    print(json.dumps(data, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
