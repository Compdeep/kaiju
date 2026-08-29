---
name: data_retrieval
description: Retrieving and examining data — logs, records, system state, query results, and gathered files
---

## Planning Guidance

When the user wants to retrieve or examine data, ask for what the question needs rather than for everything available. Where the request names a particular process, address, file, record or event, search for that directly; a broad listing followed by a search over it is two steps doing one step's work.

Compare any timestamp in a result against the current time, which is given at the top of the system state. Say when data is old and what that means for the finding, rather than presenting it as current.

A result too large to carry in a step comes back shortened, and the step says where the whole thing was written. `web_fetch` returns `full_content_path` and `bytes` alongside a `content` that has been cut to fit. Counting, searching or totalling over the shortened text gives an answer about the opening of the document and not about the document. Any step that counts, searches or totals must read the file: pass the path to `file_read`, `bash` or `compute` and open it there. Never paste a document into a parameter.

Where several sources are needed and none depends on another, plan them as separate steps with no reference between them; they run at the same time.

## Aggregator Guidance

Present the retrieved data clearly. Put the most relevant finding first. Where the data is voluminous, give the key points and say what else is there and where it is. State plainly whether the data answers the question, answers part of it, or does not answer it — and if a step returned nothing, say that rather than filling the space.
