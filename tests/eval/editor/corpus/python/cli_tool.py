import argparse
import json
import sys
from pathlib import Path


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="summarize a JSON array of events")
    p.add_argument("input", type=Path, help="path to events.json")
    p.add_argument("--field", default="type", help="field to group by")
    p.add_argument("--output", type=Path, default=None, help="write summary here; stdout if omitted")
    return p


def summarize(events: list[dict], field: str) -> dict[str, int]:
    counts: dict[str, int] = {}
    for ev in events:
        key = str(ev.get(field, "unknown"))
        counts[key] = counts.get(key, 0) + 1
    return counts


def main(argv: list[str]) -> int:
    args = build_parser().parse_args(argv)
    events = json.loads(args.input.read_text())
    summary = summarize(events, args.field)
    text = json.dumps(summary, indent=2, sort_keys=True)
    if args.output:
        args.output.write_text(text)
    else:
        print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
