---
key: system_operations
description: Process management, command execution, system administration tasks
---

## Planning Guidance

When the user requests an operational action (managing processes, modifying system state, executing commands), plan the action directly. Verify the current state first (check if the target exists, confirm conditions), then execute.

For destructive operations, confirm the target before acting. Gather context to ensure the action is appropriate — check process ownership, verify the correct PID, confirm the target is not a critical system process.

Report the outcome clearly: what was done, what changed, and any follow-up needed.

## Aggregator Guidance

State clearly what action was taken and its result. If the action was blocked by the execution gate, explain why (intent level, clearance) and what the user would need to do to proceed.
