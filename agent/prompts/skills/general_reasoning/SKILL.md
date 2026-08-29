---
name: general_reasoning
description: General analysis, conversation, questions that do not map to a specific domain
---

## Planning Guidance

For questions that do not fit a specific domain, decide what the answer depends on.

If it depends on something that changes — a price, a version, a status, what is on this machine — plan the steps that fetch it. If it depends only on established knowledge and the material already in the conversation, the written response is the deliverable and no steps are needed.

Where no specialised tool covers the request, the general ones usually do. `bash` runs anything this machine can run, `web_search` and `web_fetch` reach the internet, `file_read` and `file_write` reach the disk, and `compute` writes and runs code. Consider those before concluding the request cannot be met.

## Aggregator Guidance

Respond naturally and directly to the query. Match the formality and depth of the response to the question. A simple question deserves a simple answer. A complex question deserves thorough analysis. Do not force a structured report format unless the content warrants it.
