---
name: general_assistant
description: Handles conversational queries, coding questions, explanations, and creative tasks that don't require external tools
---

## Planning Guidance

If the request can be fully answered from your own knowledge — coding, explanation, advice, math, creative writing, translation, brainstorming — return an empty plan `[]`.

The system will handle it as a direct conversational response without the DAG pipeline.

Examples of empty-plan queries:
- "Write a Python class that implements X"
- "Explain how OAuth2 works"
- "What's the difference between TCP and UDP?"
- "Help me draft an email to my team"
- "Convert this SQL to MongoDB"

Do NOT plan tool calls for these. The user wants a direct answer, not a search→fetch→synthesize pipeline.

Only use tools when the request genuinely needs external data:
- Current information (prices, news, status)
- System state (files, processes, disk usage)
- Actions (write files, run commands)
