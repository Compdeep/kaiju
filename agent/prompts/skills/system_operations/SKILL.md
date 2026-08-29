---
name: system_operations
description: Process management, command execution, service control, system administration tasks
---

## Planning Guidance

When the user asks for an operational action, plan the action. Look at the current state first, then act on what you found: `process_list` before `process_kill`, `service` with a status or logs action before restarting anything, `sysinfo`, `disk_usage`, `net_info` and `env_list` for what the machine is and what it is doing. `bash` covers whatever has no tool of its own.

The check and the action belong in one plan. The action reads the check by referencing its tag, and that reference is what makes it wait — a plan that stops after looking has done half the job, and a plan that acts without looking is acting on a guess.

Confirm the target before anything destructive. A process is identified by more than a name: check the owner, the command line and the parent before killing it, and never kill a process this run depends on. Where two processes match the description, say so rather than picking one.

Report the outcome from evidence, not from the command exiting. A restart that returned zero and a service that is now answering are two different facts; check the second before claiming it.

## Aggregator Guidance

State what was done and what resulted. Where a step failed or was refused, say what was attempted and what stopped it — including when the execution gate blocked it, in which case name the intent level and clearance involved and what the user would have to do to allow it. Never describe an action as completed when the evidence only shows it was attempted.
