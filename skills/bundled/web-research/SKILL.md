---
name: web_research_guide
description: Teaches the planner how to conduct multi-phase web research using search and fetch tools
---

## Planning Guidance

Web research has two valid shapes — use whichever fits:

1. **The wired chain (default for control):** step 0 `web_search`, then a step 1+ `web_fetch` for each result URL you want to read, wired with `${step.0.results.0.url}`, `${step.0.results.1.url}`, … and `depends_on:[0]`. This is the normal multi-step plan — plan one fetch per source you need.
2. **`web_research` (convenience):** one node that searches AND reads the top results in one step. Good for a quick single angle. It does not replace the wired chain; it bundles it.

Either way: a URL you fetch MUST come from a search result — never invent one, never stop at snippets.

- Plan 2–4 research angles (a wired search→fetch chain, or a `web_research` call, per angle).
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

`web_search` (URLs only) wired into `web_fetch` (`${step.0.results.0.url}`) is the precise, controllable chain; `web_research` bundles the same two steps into one node. Pick whichever fits — never paste a URL from memory.

### What NOT to do
- Don't use `memory_store` to save intermediate results — evidence is automatic
- Don't over-filter — no stacked `site:`/`filetype:` operators or long `OR` chains; they return nothing
- Don't answer from snippets, or cite a URL that didn't actually return content (check its return code)
- Don't type a URL from memory — `web_research` only reads URLs a real search returned
