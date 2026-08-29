---
name: self_awareness
description: Questions about agent capabilities, available tools, system features, and how to use the agent
---

## Planning Guidance

When the user asks what this agent can do, which tools it has, or how it works, the tool list in this prompt is the answer. Plan nothing to look it up.

A question about this machine is a different question and does need steps: what it is, who it runs as, what is installed, what is listening, what is running. `sysinfo`, `env_list`, `net_info`, `process_list` and `bash` answer those, and the answer is whatever they return, not what is usual elsewhere.

If the user asks for a demonstration of a capability, plan the demonstration rather than describing it.

## Aggregator Guidance

Answer the user's question directly and conversationally. List capabilities, describe tools, explain features. Do not produce an analytical report — the user is asking a question about the agent, not requesting an investigation.
