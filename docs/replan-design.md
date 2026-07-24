# Kaiju replan — design & build plan

Status: **Phase 1 + Phase 2 built (2026-07-24).** Reflector is now a 3-way
classifier (`continue | replan | conclude`); repair flows through a `debug`
super-tool. Open: web_search reliability + URL grounding (bottom of this doc).

Phase 1 shipped the EXPAND path: the reflector emits `replan` and the scheduler
re-invokes the executive to plan the next steps and grafts them.

Phase 2 (chosen: collapse now, not the phased keep-investigate route) removed
`investigate` as a reflection decision and turned Holmes + MicroPlanner +
validators into a **`debug` super-tool** (thin trigger — grafts into the live
DAG, reusing all existing Holmes/microplanner machinery). Repair now flows
through the same door as expand: `reflect.replan → executive plans a debug step
→ scheduler grafts Holmes on the debug node's completion`. `debug` is visible to
the executive only (pruned from Holmes + microplanner — no debug-in-debug).
Landed in `builtin_debug.go` (new), `scheduler.go` (debug graft + investigate
case removed), `reflection.go`/`function_calls.go`/`prompts.md` (3-way
reflector, investigate coerced→replan), `rca.go` + microplanner graft (recursion
prune), `cmd/kaiju/main.go` (registration), and `builtin_debug_test.go` +
`reflection_replan_test.go`.

## The problem this fixes
Research runs fabricate sources. Root cause, traced through a real DAG:
- The DAG is **plan-once**: the executive commits every step up front, before any
  result exists. So it either pre-plans `web_fetch` with `${step.N.results.0.url}`
  placeholders (brittle — a `null` search breaks the ref) or skips fetching entirely.
- In one run: 18 `web_search` nodes, **0 `web_fetch`**, 10 searches returned
  `results:null`. The aggregator then invented URLs for the empties (OWASP → 404)
  **and** claimed "fully validated and verified all URLs, all directly accessible"
  — a fabricated verification it never performed (zero fetch nodes).

## Core insight
The graph grows for exactly two reasons:
- **EXPAND** — a wave *succeeded* and revealed more work (found URLs → fetch them).
  **This path does not exist today.** ← `replan` adds it.
- **REPAIR** — a step *failed* (today: `investigate` → Holmes RCA → MicroPlanner
  grafts fix → validators verify).

Repair is just replan with a diagnosis in front of it. We keep the DAG for the
parallel *triage* wave (fan out N searches — DAG is good at this, ReAct is not) and
add a replan loop for the *adapt* (ReAct-flavored, because you can't predict results).

## Architecture decisions
1. **Reflector decides, executive plans.** The reflector is a cheap classifier; it
   says *what's needed* (`next`), the executive (heavy model, tool index, skill
   guidance) decides *how*. Don't make the reflector emit steps.
2. **Anchor the question, don't reframe it.** Keep the user's goal verbatim; add a
   generic re-plan frame around it (reframing → intent drift).
3. **Context already carries.** The executive already pulls `Worklog(20)` + tool
   index + skill guidance every plan (`executive.go:670`). So a re-plan isn't blind —
   the worklog holds prior results. What's new is the FRAME + a gap line, not raw data.
4. **Task-agnostic.** The frame (goal / done / gap / plan-next) is generic; domain
   smarts ride in the skills + worklog. Same replan path serves web-fetch, coding
   fixes, security probes.
5. **Governor = English score + hard caps.** Tell the reflector its position in
   prose ("replan round 2 of 3, 3m40s elapsed") so it self-regulates; back it with a
   replan cap + the diminishing→conclude downgrade + wall clock.

## Holmes / investigate — the endgame (Option A, phased)
Current failure pipeline: `reflect.investigate → Holmes (RCA) → MicroPlanner (fix) →
validators`. Kaiju already has **super-tools** (`compute` spawns architect→coder→
validator; `agent` spawns a sub-run) — tools by interface that orchestrate DAG
sub-structure. **Holmes becomes one of these.** Repair then flows through the same
door as expand: `reflect.replan → executive plans a `debug` super-tool step` (Holmes
+ MicroPlanner + validators live inside it). Cost: one extra executive hop; gain:
unified growth path + composability + executive picks trivial-retry vs full-debug.

**Phase it — do NOT rip out the working debug pipeline while building replan:**
- **Phase 1 (DONE, 2026-07-24):** add `replan` ALONGSIDE the existing `investigate`.
  Expand path working. Failure pipeline untouched.
- **Phase 2 (DONE, 2026-07-24):** migrated `investigate` → a `debug` super-tool,
  `replan` drives repair too, deleted the special reflection branch. Guards
  survived: the "don't-investigate transient/unfixable" rules moved into the
  REFLECTOR's "Don't replan for a failure — conclude instead" section and the
  `debug` tool description; Holmes's Step-0 no-crime guard stayed in the HOLMES
  prompt (Holmes still runs). Chosen over the phased keep-investigate route.

## Phase 1 — concrete build
### Reflector prompt (drafted, needs a final eyeball — the replan-vs-conclude
### paragraph is the anti-hallucination lever)
Four decisions: `continue | replan | conclude | investigate`.
- **replan** — steps SUCCEEDED and revealed the next move; goal not answered yet, no
  failure; put the concrete next step in `next`.
- **conclude ONLY when the evidence ANSWERS the goal.** If results merely POINT at it
  (unfetched URLs, unfollowed leads), that's replan, not conclude. Never fill the gap
  from memory — an unfetched URL is not a verified source. When torn, replan.
- **investigate** — a step FAILED and needs root-cause diagnosis first.
- Budget: told rounds spent + time; every round must materially improve; else conclude
  and name what's missing. `progress:diminishing` after 2 → stop.
- Output JSON adds a `next` field (parallel to `problem`).

### Code changes
1. `reflectionOutput` struct — add `Next string \`json:"next"\``; accept `replan` in
   `parseReflectionOutput` (currently only continue/conclude/investigate).
2. `assembleReflectorPrompt` — inject the budget-in-English line (replan round X of Y,
   elapsed).
3. `scheduler.go` — a `case "replan":` that re-invokes the executive with
   [verbatim goal + reflector's `next` + "you already ran the worklog below; plan only
   the NEXT steps, don't repeat done work"], grafts the returned nodes, continues loop.
4. Replan counter (cap ~3, same pattern as `max_investigations`) + reuse the
   diminishing→conclude downgrade as the hard backstop.
5. Leave `investigate`/Holmes/MicroPlanner alone (that's phase 2).

## Parallel workstreams (independent of replan)
- **`web_search` reliability** — it scrapes Startpage→DDG HTML; both block the server
  IP → ~10/18 `null`. Move to a real search backend (Brave/Tavily/SerpAPI) or keyed
  provider; dedup; drop dead results; return an explicit "no results", never a bare
  `null` that becomes fiction. Keep `web_search` and `web_fetch` SEPARATE (merging
  orphans web_fetch; auto-fetch-inside-search is wasteful + kills the reasoning step).
- **URL grounding** — once fetch actually pulls chosen URLs, forbid citing any URL the
  run didn't fetch; forbid "verified/accessible" claims unless a fetch confirmed it.

## Key files
- `internal/agent/prompt/prompts.md` → `=== REFLECTOR ===` (built-in; not overridden
  for makeen).
- `internal/agent/reflection.go` → `reflectionOutput`, `parseReflectionOutput`,
  `assembleReflectorPrompt`, `fireReflection`.
- `internal/agent/scheduler.go` → the reflection-decision switch (`case "continue"` ~980,
  `case "conclude"`, the diminishing downgrade ~964); executive re-invoke via
  `runExecutive`/`runPlanAndSchedule`.
- `internal/agent/executive.go:670` → the executive's ContextGate sources (Worklog +
  tool index) — already carries prior results.
- `internal/tools/web_search.go` → the scraper (Startpage→DDG), where the `null` comes from.
