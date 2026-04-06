# KAIJU

**Kaiju is an AI assistant that plans before it acts.** It builds structured workflows, executes tasks in parallel, and completes complex work reliably instead of responding step by step.

## Overview

Kaiju turns requests into executable plans and follows them through to completion.

Instead of working one step at a time, it organizes the problem upfront, breaks it into tasks, and tracks execution until the result is delivered. This makes it more reliable on multi-step and technical work where sequencing and coordination matter.

## What It Does

Kaiju executes tasks directly using tools.

It comes preloaded with tools for web search, file management, code generation and execution, process control, git operations, network diagnostics, and system administration. It does not just suggest actions — it performs them and returns results.

The tool system is extensible. Add a drone control tool and Kaiju becomes an autonomous sentry. Add a trading API and it becomes a portfolio manager. Add a deployment tool and it manages your infrastructure. Each new tool immediately becomes available to the planner, the execution engine, and the security model — no integration code required.

Work is split into independent tasks where possible, allowing multiple operations to run at the same time. This improves both speed and robustness.

## Execution Model

Kaiju uses a graph-based execution system.

Each task is represented as a node with defined inputs and outputs. Nodes can run in parallel or depend on each other. The system monitors execution, retries failures with accumulated context, and updates the plan when needed.

For complex implementation work, an architect node designs the solution, defines the interfaces between components, and delegates to parallel coding nodes that each implement their part against the shared specification. This is how Kaiju builds software — not by writing one file at a time, but by planning the architecture and executing in parallel.

This structure allows it to handle workflows that are difficult for purely sequential systems.

## Security

All actions are checked at execution time.

Kaiju's security is powered by IBE (Intent-Based Execution), which takes intent as an input and enforces that intent as a policy on every action. The system evaluates each operation against what the user is allowed to do, what they intended, and how impactful the action is. If it exceeds those limits, it is blocked. If it doesn't, it proceeds — automatically, without human interaction, in seconds.

This enforcement is built into the execution engine itself, not controlled by prompts, so it cannot be bypassed by the AI regardless of how it is prompted. This is what makes autonomous operation possible — Kaiju can be given broad authority to act because the security boundary provides a mathematical guarantee that it will not exceed its authorized scope. It will not go rogue.

## Platform

Kaiju runs as a single lightweight binary (14MB) with an integrated web interface.

It works across Linux, macOS, Windows, and ARM devices (Raspberry Pi, Jetson, edge hardware). It connects to any OpenAI-compatible language model API — OpenAI, Anthropic, OpenRouter, or local models via Ollama for air-gapped operation. It includes a web dashboard, REST API, SSE event streaming, and a workspace with code editor, file browser, and live preview.

No external dependencies. No container orchestration. One binary, one config file.

---

# Use Cases

## Analyst Assistant

Parallel information gathering and structured analysis. Kaiju searches multiple sources simultaneously, cross-references findings, and synthesizes results with citations. Set intent to observe and the agent can access everything but modify nothing — guaranteed by IBE.

**Example**: "Investigate the Log4Shell vulnerability — what systems are affected, what patches exist, and what is our exposure." Kaiju searches CVE databases, vendor advisories, and internal asset inventories in parallel, correlates the findings, and delivers a structured risk assessment.

## Developer Platform

Full-stack development through the architect-coder pipeline. The architect designs the solution and defines interfaces. Parallel coding nodes implement each component against the shared spec. The workspace provides syntax-highlighted editing with autocomplete and live preview for web applications.

**Example**: "Build a Vue 3 dashboard with JWT authentication and a Postgres backend." Kaiju architects the project, sets up the environment, and delegates frontend, backend, database, and authentication to parallel workers — each implementing against the same API contract.

## Autonomous Sentry

Continuous monitoring with autonomous response within strict security bounds. Kaiju evaluates threats, gathers evidence, and takes graduated action — observe by default, operate on confirmed threat, override only with explicit clearance. Paired with the gossip mesh (libp2p) for multi-node coordination.

**Example**: A fleet of Kaiju instances monitoring network segments. Each node observes locally, shares findings via pub/sub, and escalates to a coordinator node that has override clearance to isolate compromised hosts. Every action is gated — a compromised node cannot escalate its own clearance.

## Edge Compute Agent

Local execution of structured workflows on constrained hardware. Single binary deployment, 30MB runtime memory, local LLM support for air-gapped environments. Processes sensor data, makes decisions, and executes actions within strict IBE bounds.

**Example**: A Raspberry Pi on a drone running Kaiju with a local model. It processes camera feeds, plans survey routes, and triggers alerts — all within observe intent. It cannot modify flight controls or access external networks because IBE enforces the boundary at every action, every time, without exception.
