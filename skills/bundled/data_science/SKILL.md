---
name: data_science
description: "Python data analysis, statistics, and data science workflows. Use when the goal involves loading data, cleaning, aggregating, computing statistics, building models, or producing plots and reports."
---

## When to Use

Use when the goal involves any of:
- loading and cleaning data (CSV, JSON, Parquet, Excel, SQL, APIs)
- exploratory data analysis (EDA) — distributions, correlations, summaries
- statistical analysis — hypothesis tests, regression, significance
- data transformation and aggregation (groupby, pivot, join, resample)
- visualization — matplotlib, seaborn, plotly charts
- machine learning — scikit-learn, training and evaluation
- time series analysis
- generating reports or notebooks with analysis narratives
- reading data from an API, database, or file and producing insights

Do NOT use for:
- web applications or UIs (use `webdeveloper`)
- generic scripting not about data
- pure math without data (just use compute directly)

## Planning Guidance

Data science work is usually a small number of scripts with rich internal logic, not a sprawling file tree like a webapp. The right shape is:

1. **One or two data-loading + cleaning scripts** (ingestion, normalization).
2. **One or two analysis scripts** (the actual computation).
3. **A plotting or reporting script** (figures, tables, or a markdown summary).
4. **Optionally a shared utilities module** if multiple scripts reuse helpers.

A typical data analysis task is 3–6 files, not 20. Don't over-decompose. But each file should be substantive — real loading, real cleaning, real computation, real visualization — not three-line stubs.

**Planning notes:**

- Use `compute` with `mode:"deep"` and `skill:"data_science"` for any non-trivial analysis (more than one step). Use `mode:"shallow"` only for single self-contained computations.
- Pass the data source in the goal or via `context` — file path, URL, database query, or API endpoint.
- If the data needs to be fetched first (HTTP or database), plan a fetch step BEFORE compute so the file is on disk and the compute can focus on analysis.
- If the user wants a report, include a final "generate report" task that produces either a markdown file or a rendered HTML/PDF from a notebook.

## Architect Guidance

Decompose data science projects into small, focused Python scripts. Each script does one job well. The pipeline is linear: ingest → clean → analyze → visualize → report.

**Typical layout:**
```
project/
  data/
    raw/            (original, immutable inputs)
    processed/      (cleaned, ready for analysis)
    outputs/        (figures, tables, reports)
  src/
    load.py         (read raw data, handle encodings, parse dates)
    clean.py        (drop nulls, fix types, normalize, handle outliers)
    analyze.py      (the main computation — groupby, stats, model)
    plot.py         (figures — matplotlib or seaborn)
    report.py       (assemble final output — markdown or HTML)
    utils.py        (shared helpers — only if genuinely reused)
  requirements.txt
  README.md
```

For smaller analyses, collapse layers — a single `analysis.py` that does load+clean+analyze, plus a `plot.py`, is fine.

**Scaffolding.** Include `pip install` for the dependencies the scripts will use:
```
"setup": [
  "python3 -m venv project/.venv",
  "project/.venv/bin/pip install pandas numpy matplotlib seaborn scikit-learn"
]
```

**Interfaces.** In data science, interfaces are less about API contracts and more about data schemas. Use the `interfaces` field to lock the column structure between pipeline stages:
```
"interfaces": {
  "raw_input": {"columns": ["user_id", "event_time", "event_type", "amount"], "source": "data/raw/events.csv"},
  "cleaned": {"columns": ["user_id", "timestamp", "event", "amount_usd"], "location": "data/processed/events_clean.parquet"},
  "analysis_output": {"columns": ["user_id", "total_events", "total_spend"], "location": "data/outputs/user_summary.csv"}
}
```
Each script reads from the previous stage's output and writes to its own stage's location. No ambiguity about column names or file paths.

**Schema is not database schema here** — use `interfaces` for data shape. Omit `schema` unless the project genuinely uses a database.

**Execute fields.** For pipelines that should run in sequence, each task's `execute` runs its script:
```
{"goal": "load and parse raw CSV", "task_files": ["src/load.py"], "execute": "cd project && .venv/bin/python -m src.load"}
{"goal": "clean and normalize", "task_files": ["src/clean.py"], "execute": "cd project && .venv/bin/python -m src.clean", "depends_on_tasks": [0]}
```

The scheduler grafts the executes in dependency order, so the full pipeline runs automatically.

**Task decomposition pattern — example: "Analyze sales data and show me trends":**
1. src/load.py (read CSV, handle encoding, parse dates, save parquet)
2. src/clean.py (drop invalid rows, fix dtypes, normalize currency)
3. src/analyze.py (monthly aggregates, YoY growth, top products, category breakdown)
4. src/plot.py (line chart of monthly trend, bar chart of top categories, heatmap of seasonality)
5. src/report.py (markdown report combining numbers and figures)

Five tasks, each substantive.

**Task decomposition pattern — example: "Train a classifier on the iris dataset":**
1. src/load.py (load via sklearn.datasets or from file, train/test split)
2. src/train.py (fit model, cross-validate, save artifact)
3. src/evaluate.py (accuracy, precision, recall, confusion matrix, save metrics)
4. src/plot.py (confusion matrix heatmap, ROC curve, feature importance)

Four tasks.

## Coder Guidance

Write Python that a data scientist reviewing your code would respect. Not jupyter-notebook-pasted-into-a-file quality — real scripts.

**Every script is self-contained and runnable:**
- Has a clear entry point, either `if __name__ == "__main__":` or a `main()` function.
- Uses argparse or hardcoded paths clearly at the top — no magic strings buried in the middle.
- Prints progress to stderr or stdout so the user knows what's happening (`print(f"Loaded {len(df)} rows", file=sys.stderr)`).
- Saves outputs to disk at predictable paths — don't print-and-lose a computed dataframe.

**Data loading:**
- Handle encoding explicitly (`encoding='utf-8'` or `encoding='latin-1'` if needed).
- Parse dates on load, not after (`parse_dates=['timestamp']`).
- Specify dtypes for known columns to avoid inference cost and errors.
- Check file exists before reading; fail with a clear message if not.
- For large files, consider chunking or using parquet/feather over CSV.

**Data cleaning:**
- Don't silently drop rows — log the count dropped and why.
- Handle NaN explicitly: decide per column whether to drop, fill, or keep.
- Validate after cleaning: assert column presence, row count sane, no duplicate keys.
- Write cleaned data to a new file, never overwrite raw input.

**Analysis:**
- Use vectorized operations — groupby, agg, apply with axis. Never iterate rows with a for loop unless you truly need to.
- Name intermediate variables clearly (`monthly_sales`, not `df2`).
- Compute one thing per variable — don't chain 15 operations in one unreadable line.
- When using scikit-learn: fit on train, transform on test. Never fit on test. Save the fitted model.
- Set random seeds where applicable so results are reproducible.

**Plotting:**
- Use matplotlib or seaborn — pick one style.
- Every figure has a title, axis labels with units, and a legend if multiple series.
- Save figures to disk (`plt.savefig(..., dpi=150, bbox_inches='tight')`) before showing or closing.
- Close figures (`plt.close()`) to avoid memory buildup in loops.
- Use readable fonts — default sizes are too small for presentations. `plt.rcParams['font.size'] = 12` or higher.
- Pick sensible colors — avoid default matplotlib's rainbow for categorical data. Use colorblind-safe palettes.

**Statistical rigor:**
- State assumptions — normality, independence, equal variance — before applying a test.
- Report effect sizes alongside p-values. A significant result with a tiny effect is not interesting.
- Use appropriate tests — don't default to t-test for everything. Wilcoxon for non-normal, chi-square for categorical, etc.
- Multiple comparisons → correct (Bonferroni or FDR) when doing many tests.

**Machine learning:**
- Train/test split before any preprocessing that uses the data (no leakage).
- Cross-validate where practical; report mean and std of CV scores.
- Show a baseline (majority class, mean, zero model) for comparison.
- Evaluate with the right metric — accuracy for balanced classes, F1/AUC/precision-recall for imbalanced.
- Save the fitted model with joblib or pickle; save the preprocessing pipeline alongside.

**Output files:**
- CSV for small tabular results users might open in Excel.
- Parquet for intermediate data between pipeline stages (fast, typed).
- PNG or SVG for figures.
- JSON for structured metrics/metadata.
- Markdown for reports.

**Never ship:**
- Hardcoded absolute paths to your own machine (`/Users/you/Desktop/...`).
- `import *`.
- `print(df)` left behind as debugging.
- Raw `except:` without an exception type.
- Untyped magic numbers (put them in a constant at the top with a comment).
- A dataframe that never gets saved anywhere — if you computed it, write it to disk or print a meaningful summary.
- Plots that are saved but never labeled or titled.
