---
name: web_research_guide
description: Teaches the planner how to conduct multi-phase web research using search and fetch tools
---

## Planning Guidance

When the request requires web research, plan in two phases:

### Phase 1: Parallel Searches
Plan multiple `web_search` calls in parallel — one per research angle.
- Use specific, targeted queries. Not "subscription boxes" but "top subscription box competitors pricing comparison 2026"
- Plan 2-5 parallel searches covering different aspects of the question

### Phase 2: Targeted Fetches
Plan `web_fetch` calls that depend on the search steps via `param_refs`.
- Use `format=summary` with a specific `focus` param for each fetch
- Chain URLs: `"param_refs": {"url": {"step": 0, "field": "results.0.url"}}`
- Plan 3-5 fetches per search, covering the top results
- Use a BROAD focus that covers all needed information in ONE fetch per URL
- NEVER fetch the same URL twice with different focus params

### Focus Parameter Examples
Good focus values (specific, covers multiple needs):
- "company name, pricing tiers, key features, target customers, competitive advantages"
- "GDPR requirements, data residency rules, enforcement examples"
- "market size, growth rate, key players, pricing trends"

Bad focus values (too narrow, causes duplicate fetches):
- "pricing" (too narrow — combine with features and customers)

### What NOT to do
- Don't use `memory_store` to save intermediate results — evidence is automatic
- Don't plan a single broad search — break it into parallel specific queries
- Don't skip the fetch step — search snippets alone are too thin
- Don't chain web_fetch → web_fetch — fetch consumes URLs, it doesn't produce them
