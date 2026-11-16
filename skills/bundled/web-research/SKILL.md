---
name: web_research_guide
description: Guides web research using web_search, web_fetch, and web_research, including source selection, multi-angle research, and whole-page analysis.
groups: [web]
---

## Planning Guidance

Evidence is page content that was actually retrieved. A search result points
at a page; it is not the page.

- Never type a URL from memory. A fetched URL must come from the user or from
  a search result.
- To read a page discovered through search, pass that search result's URL to
  `web_fetch`.
- `web_research` searches and reads the leading results in one step, for an
  angle where choosing the sources adds little.
- Plan one angle per distinct question. Independent angles run in parallel.
- A `focus` names every fact needed from the page, so one read collects them.
  `format: extract` quotes them word for word.
- `content` is cut to fit a prompt. To count, search or total over a whole
  page, pass `full_content_path` to `file_read`, `bash` or `compute`.
- A fetch that failed or returned nothing is not evidence. Its error says what
  was wrong; read it before trying again.
- When the sources do not hold the answer, say so. Missing data is a finding:
  never fill the gap from memory or a guess.
