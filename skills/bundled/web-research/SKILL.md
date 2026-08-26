---
name: web_research_guide
description: Teaches the planner how to research with web_search, web_fetch and web_research — choosing a shape, following a trail, and working on whole pages
---

## Planning Guidance

### Purpose

Use web research when the answer needs current information, outside evidence, or precise attribution.

Answer only from page content that was actually retrieved. Not from search snippets, not from a remembered URL.

### Choosing a shape

Use the smallest shape that answers the question.

**Wired chain** — the default when source choice or traceability matters.

1. A `web_search` step for one angle.
2. A `web_fetch` step for each result worth reading. Each takes its URL from the search output, declares that search in `depends_on`, and names the facts to extract in `focus`.

**`web_research`** — one node that searches and reads for a single angle. Same evidence rules. Use it when picking sources individually adds nothing.

Never type a URL from memory. A URL you fetch comes from a search result.

### Planning angles

Plan by distinct question, not by a source count.

- A simple factual question: one angle.
- A comparison or recommendation: two or three.
- A broad or disputed question: three or four.

Avoid angles that will return the same evidence twice.

Per angle: a plain keyword query, `max_sources` of 3–5, `focus` naming every fact needed from a page, and `recency_days` only when freshness matters.

### Following a trail

Some answers cannot be planned in advance. You have to see one result before you can write the next step — find the API endpoint, then call it; find the filing, then pull a figure out of it.

When a step reveals what comes next, you will be asked to plan again with its result in view. Write the next step then.

### Whole pages

`web_fetch` returns two forms of the same page:

- `content` — shortened so it fits in a prompt. On a long page this is only the opening.
- `full_content_path` — the whole page, saved to disk. `bytes` is its size.

Compare `bytes` with the length of `content` to see how much you were given.

To count, search or total across a whole page, pass `full_content_path` to a `bash` or `compute` step and open the file there. Counting inside `content` gives a number for the opening, not for the page.

Never paste a document into a parameter. It goes into a prompt, and a large one does not fit.

### Queries

Plain keywords: `sovereign AI infrastructure market size 2026`.

Not stacked operators: `site:… OR site:… filetype:pdf`. They over-filter and return nothing. One `site:` is acceptable when the task names an official source.

### Focus

Name every related fact you need from a page, so it is read once.

- Good: `pricing tiers, key features, target customers, competitive advantages`
- Good: `market size, forecast period, growth rate, methodology, named competitors`
- Too narrow: `pricing`, `market size`

### Choosing sources

Prefer primary sources — official documentation, filings, legislation, papers, datasets, company announcements. Then secondary sources that show where their figures came from. Others only when no primary evidence exists.

For a disputed claim, look for independent corroboration rather than several pages repeating one original. Drop syndicated copies of the same article.

### When a source cannot be read

A page counts as evidence only if content came back. If it did not, discard it, then either fetch another result from the same search or run a narrower search.

Never cite a page that returned nothing.

### When to stop

Stop when every material part of the question is supported, disputed claims have corroboration, and further sources would repeat what you have.

Do not add steps to reach a number.

### Do not

- Save intermediate results with `memory_store` — evidence is kept automatically.
- Fetch the same page repeatedly for facts a single `focus` could have gathered.
- Answer from snippets, or cite a page whose content was not retrieved.
