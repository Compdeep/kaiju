---
name: web_research_guide
description: Teaches the planner how to conduct multi-phase web research using search and fetch tools
---

## Planning Guidance

For web research, use the **`web_research`** tool. It runs a search AND reads the top result pages in ONE step, returning their actual content — so every source is grounded (the URLs come from the search) and already read for you. With `web_research` you never plan a separate `web_search` + `web_fetch`, never pick or invent a URL, and never stop at snippets.

- Plan 2–4 parallel `web_research` calls, one per research angle.
- Use plain KEYWORD queries, not search operators. "sovereign AI infrastructure market size 2026", NOT `site:… OR site:… filetype:pdf …`. Stacked `site:`/`filetype:` operators over-filter and return nothing — the engine ranks good sources without them.
- `max_sources` = how many top results to read per angle (default 4). `recency_days` biases to recent results for time-sensitive figures. `focus` names the exact facts/figures to pull from each page.
- Each source `web_research` returns shows its **return code** and its content. Answer ONLY from sources that actually returned content — if a source shows a 4xx/blocked code with no content, it was NOT read: do not cite it, and never present an unread or invented URL as a source.

### Focus Parameter Examples
Good focus values (specific, covers multiple needs):
- "company name, pricing tiers, key features, target customers, competitive advantages"
- "GDPR requirements, data residency rules, enforcement examples"
- "market size, growth rate, key players, pricing trends"

Bad focus values (too narrow, causes duplicate fetches):
- "pricing" (too narrow — combine with features and customers)

`web_search` (URLs only) and `web_fetch` (one specific known URL) still exist for the rare cases that need them — e.g. fetching a URL the user handed you. For everything else, use `web_research`.

### What NOT to do
- Don't use `memory_store` to save intermediate results — evidence is automatic
- Don't over-filter — no stacked `site:`/`filetype:` operators or long `OR` chains; they return nothing
- Don't answer from snippets, or cite a URL that didn't actually return content (check its return code)
- Don't type a URL from memory — `web_research` only reads URLs a real search returned
