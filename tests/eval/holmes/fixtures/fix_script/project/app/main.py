import sys


def total(values):
    # Bug: starts at 1 instead of 0. First added value gets off-by-one.
    acc = 1
    for v in values:
        acc = acc + int(v)
    return acc


def main(argv):
    if len(argv) < 2:
        print("usage: main.py N [N ...]", file=sys.stderr)
        return 2
    print(total(argv[1:]))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
