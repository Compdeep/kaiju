---
name: precise_research
description: "Research questions that require a specific factual answer — a number, name, date, or short phrase. Use when the question asks for an exact value that must be verified through web search, API calls, or data extraction."
---

## When to Use

Use when the question asks for a precise factual answer: a count, a name, a date, a measurement, a title, a code, or any specific value. Most research questions fall into this category.

## Planning Guidance

### Wikipedia or encyclopedia lookups

Many questions reference Wikipedia directly or ask about well-documented facts.

1. `web_fetch` — fetch the specific Wikipedia page with `format=summary` and a focused `focus` parameter targeting the exact data point
2. If the page is long, fetch it and then search the file it saved. `web_fetch` returns `full_content_path`, which is the whole page on disk. Use `bash` with `grep` on that file. Do not use `format=raw` — it no longer exists.

### GitHub data

Questions about GitHub issues, commits, dates, or contributors.

1. `bash` — use `gh` CLI: `gh issue list`, `gh issue view`, `gh api repos/owner/repo/issues`
2. For specific issue labels or dates: `bash` — `gh api "repos/numpy/numpy/issues?labels=Regression&state=closed&sort=created&direction=asc&per_page=1"`

### Counting and enumeration

Questions that ask "how many" require finding a complete list and counting.

1. `web_search` — find the authoritative source
2. `web_fetch` — fetch it
3. `bash` or `compute` — count over the file at `full_content_path`, not over `content`

`content` is shortened so it fits in a prompt. On a long page it holds only the
opening. Counting in it gives a count for that opening, not for the document.

`full_content_path` is the whole page on disk. `bytes` is its size. Compare
`bytes` with the length of `content` to see how much you were given.

A step that counts, searches, or totals must read the file. Pass
`full_content_path` to that step and open it there. Never paste the document
into a parameter — it would go into a prompt, and a large one does not fit.

### Date lookups

Questions asking "when was" need exact dates in the format the source provides.

1. `web_search` — find the source
2. `web_fetch` — extract the exact date string
3. Do NOT reformat dates — return them exactly as the source states

### Calculations

Questions requiring math (distance, time, percentage, volume).

1. `web_search` — find the raw numbers
2. `bash` — `python3 -c "print(result)"` to compute the exact answer
3. Plan the search and computation as dependent steps

### Cross-referencing

Questions that chain facts: "the person who did X also did Y, what is Z?"

1. `web_search` — find X (step 0)
2. `web_fetch` — extract the person/item from X (depends on step 0)
3. `web_search` — search for Y using the value extracted in step 1 (depends on step 1)
4. `web_fetch` — extract Z (depends on step 2)

### ArXiv and academic papers

1. `web_fetch` — fetch `https://arxiv.org/abs/<id>` directly with `format=summary`
2. For specific data within papers: `web_fetch` with a targeted `focus` parameter

### Video content questions

Questions about YouTube videos — timestamps, counts, dialogue.

1. `web_fetch` — fetch the YouTube page to get the description and metadata
2. `bash` — use `yt-dlp --write-auto-sub --skip-download` to get transcripts if needed
3. For specific timestamps: fetch transcript and search with bash grep

### Museum and collection lookups

1. `web_fetch` — fetch the collection page directly using the accession/museum number in the URL
2. Focus on the specific data point asked about

### What NOT to do

- Don't return a range when the question asks for a single number
- Don't guess when the evidence is insufficient — search more specifically
- Don't skip the computation step when math is required — use bash with python
- Don't fetch a general overview page when a specific data page exists
- Don't plan one broad search when the question needs a specific lookup
