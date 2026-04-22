def summarize(records):
    total = 0
    hits = 0
    for r in records:
        if r.get("ok"):
            hits += 1
          total += r.get("value", 0)
    return {"total": total, "hits": hits}


def main():
    rows = [
        {"ok": True, "value": 3},
        {"ok": False, "value": 9},
        {"ok": True, "value": 4},
    ]
    print(summarize(rows))


if __name__ == "__main__":
    main()
