---
name: web_research_guide
description: Guides web research using web_search, web_fetch, and web_research, including source selection, multi-angle research, and whole-page analysis.
---

## Planning Guidance

### Purpose

Use web research when the answer requires current information, external
evidence, or precise attribution.

Evidence must come from page content that was actually retrieved. Search
snippets and remembered URLs are not evidence.

### Choose the research shape

Use the smallest shape that can answer the question.

**Wired chain** — use when source selection, traceability, or follow-up work
matters:

1. Search for one research angle with `web_search`.
2. Fetch each useful result with `web_fetch`.
3. Pass each URL from the search output using a step reference.

The reference creates the dependency. Do not add `depends_on`.

Example:

```json
[
  {
    "tool": "web_search",
    "params": "{\"query\":\"official PostgreSQL replication documentation\",\"max_results\":4}",
    "tag": "find_replication_docs"
  },
  {
    "tool": "web_fetch",
    "params": "{\"url\":\"<the first result URL find_replication_docs returned>\",\"format\":\"summary\",\"focus\":\"supported replication modes, requirements, and limitations\"}",
    "tag": "read_replication_docs"
  }
]
```

**`web_research`** — use for a single research angle when individually choosing
sources adds little value. It searches and reads the leading results in one
step.

Never type a URL from memory. A fetched URL must come from the user or from a
search result.

### Research angles

Plan by distinct questions, not by an arbitrary source count:

* Simple factual question: one angle.
* Comparison or recommendation: two or three angles.
* Broad, consequential, or disputed question: three or four angles.

Avoid angles likely to return the same evidence.

Use plain, specific queries. Set:

* `max_results` for `web_search`;
* `max_sources` for `web_research`;
* `focus` to name all related facts needed from each page; and
* `recency_days` only when freshness matters.

Independent angles may run in parallel.

### Following a trail

When one result supplies the URL needed by another step, wire that value forward
and include both steps in the same plan whenever the later operation can already
be specified.

Replanning is appropriate only when the first result must be inspected before
the next query or operation can be formulated. A trail is not a reason to stop
when a reference can carry the discovered value forward.

### Whole pages

`web_fetch` returns:

* `content`: the extracted text that fits in the step result; and
* `full_content_path`: the saved page content.

For work that must cover the entire page—such as counting, searching, extracting
all matching records, or calculating totals—pass `full_content_path` to
`file_read`, `bash`, or `compute`.

Do not perform whole-document analysis on shortened `content`, and do not paste
a large document into another step's parameters. Pass its path instead.

### Queries and focus

Prefer plain keyword queries:

```text
sovereign AI infrastructure market size 2026
```

Avoid unnecessarily stacked operators that may eliminate useful results. Use a
single `site:` filter when the task specifically requires an official or named
source.

A `focus` should request all related facts needed from the page in one read.

Good:

```text
pricing tiers, key features, target customers, and stated limitations
```

```text
market size, forecast period, growth rate, methodology, and named competitors
```

Too narrow:

```text
pricing
```

### Source quality

Prefer, in order:

1. Primary sources such as official documentation, filings, legislation,
   research papers, datasets, and company announcements.
2. Secondary sources that identify their underlying evidence.
3. Other sources only when stronger evidence is unavailable.

For disputed claims, seek independent corroboration. Do not count syndicated
copies or pages repeating the same original source as separate confirmation.

### Failed retrieval

A page is evidence only when usable content was returned.

If a page cannot be read, discard it and fetch another result from the existing
search. Run a narrower search only when the existing results are unsuitable.

Never cite or rely on a page that returned no usable content.

### When to stop

Stop when:

* every material part of the question has supporting evidence;
* consequential or disputed claims have appropriate corroboration; and
* further sources would only repeat existing evidence.

Do not add steps merely to reach a target source count.

### Do not

* Answer from search snippets.
* Cite pages that were not successfully retrieved.
* Fetch the same page repeatedly when one sufficiently broad `focus` can collect
  the required facts.
* Save intermediate evidence with `memory_store`; step results are preserved
  automatically.
