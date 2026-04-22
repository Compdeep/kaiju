import pandas as pd
from pathlib import Path


def load_events(path: Path) -> pd.DataFrame:
    df = pd.read_csv(path)
    df["timestamp"] = pd.to_datetime(df["timestamp"])
    return df


def daily_totals(df: pd.DataFrame) -> pd.DataFrame:
    df = df.copy()
    df["date"] = df["timestamp"].dt.date
    grouped = df.groupby("date").agg(
        events=("event_id", "count"),
        revenue=("revenue", "sum"),
    )
    return grouped.reset_index()


def save_report(df: pd.DataFrame, out: Path) -> None:
    df.to_csv(out, index=False)


if __name__ == "__main__":
    src = Path("events.csv")
    dst = Path("daily.csv")
    events = load_events(src)
    report = daily_totals(events)
    save_report(report, dst)
