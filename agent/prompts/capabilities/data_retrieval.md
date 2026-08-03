---
key: data_retrieval
description: Telemetry queries, log analysis, system state retrieval, and data gathering
---

## Planning Guidance

When the user wants to retrieve or examine data, plan efficient queries. Use targeted search tools with specific parameters rather than retrieving all data. If the query references a specific process, IP, or event, search for that directly.

Assess data freshness — compare telemetry timestamps against the current time (use get_context). Note when data is stale and what that implies for the findings.

## Aggregator Guidance

Present the retrieved data clearly. Highlight the most relevant findings first. If the data is voluminous, summarise the key points and note what else is available. State whether the data fully answers the query or if additional retrieval is needed.
