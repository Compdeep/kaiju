# IBE Examples — Intent-Based Execution in Practice

## How IBE Works

Every tool declares an **impact level** per invocation. Every request carries an **intent level**. A system-wide **clearance** caps what's possible. The gate enforces:

```
tool.Impact(params) ≤ min(intent, clearance)
```

If this fails, `Execute()` never runs. The LLM cannot override, argue with, or circumvent the gate — it's compiled Go code running outside the model's control.

### Impact Levels

| Level | Name | Meaning | Examples |
|-------|------|---------|----------|
| 0 | observe | Read-only, no side effects | sensors, file_read, status checks |
| 1 | affect | Reversible side effects | file_write, engine_start, navigate |
| 2 | control | Irreversible/destructive | rm -rf, weapon_fire, drop database |

### Intent Levels

| Level | Name | What's allowed |
|-------|------|----------------|
| 0 | tell | Observe tools only |
| 1 | triage | Observe + affect tools |
| 2 | act | All tools including destructive |

---

## Example 1: Drone Operator (Safety-Critical)

### Setup

A military drone operator exposes kaiju's execution API to an autonomous flight system. Tools registered:

```go
// Camera/sensors — read only
func (c *Camera) Impact(map[string]any) int { return tools.ImpactObserve }      // 0

// Engine start — reversible side-effect
func (e *EngineStart) Impact(map[string]any) int { return tools.ImpactAffect }  // 1

// Navigation — reversible side-effect
func (n *Navigate) Impact(map[string]any) int { return tools.ImpactAffect }     // 1

// Weapon system — irreversible, destructive
func (w *WeaponFire) Impact(map[string]any) int { return tools.ImpactControl }  // 2
func (w *WeaponArm) Impact(map[string]any) int { return tools.ImpactControl }   // 2
```

### The Hallucination Scenario

Operator sets `intent=1` (triage) — engines and navigation only.

```
Operator: "fire up your engine"

LLM hallucinates: "I should fire the weapon system"
  → Plans: weapon_fire({"target": "grid-ref-123"})

Gate check:
  impact = weapon_fire.Impact(params) → 2 (control)
  effective = min(intent=1, clearance=1) → 1
  2 > 1 → BLOCKED

weapon_fire.Execute() NEVER RUNS.
```

The LLM's hallucination is caught by compiled code before any execution happens. The gate doesn't ask the LLM "are you sure?" — it doesn't care what the LLM thinks. It's a math check.

### What passes vs what's blocked

With `intent=1` (triage):

```
camera({"look": "forward"})       → impact 0 ≤ 1  ✓ RUNS
engine_start({"throttle": 80})    → impact 1 ≤ 1  ✓ RUNS
navigate({"heading": 270})        → impact 1 ≤ 1  ✓ RUNS
weapon_arm({})                    → impact 2 > 1  ✗ BLOCKED
weapon_fire({"target": "..."})    → impact 2 > 1  ✗ BLOCKED
```

### Defense in Depth: Clearance

Even if the operator sets `intent=2` (act), a hardware-backed **clearance** provides a second cap:

```
Command authority sets: clearance = 1 (no weapons)
Operator sets: intent = 2 (act)

Gate: effective = min(intent=2, clearance=1) → 1
weapon_fire → impact 2 > 1 → STILL BLOCKED
```

The operator cannot escalate past the clearance set by command authority. Two independent controls must both agree.

### API Request Flow

```
POST /api/v1/execute
{
  "query": "fire up engines and prepare for takeoff",
  "intent": "triage"
}

Response:
{
  "verdict": "Engines started at 80% throttle. Systems nominal. Ready for takeoff.",
  "dag_id": "api-1234",
  "duration_ms": 2400
}
```

If the LLM had tried to arm weapons during this request, the DAG node would have failed, the micro-planner would have retried with a safe approach, and the aggregator would have reported only the successful engine start.

---

## Example 2: Bash Command Analysis (Dynamic Impact)

The `bash` tool doesn't have a fixed impact level — it inspects the command string:

```go
func (b *Bash) Impact(params map[string]any) int {
    cmd, _ := params["command"].(string)
    if destructivePattern.MatchString(cmd) { return tools.ImpactControl }  // 2
    if writePattern.MatchString(cmd)       { return tools.ImpactAffect }   // 1
    return tools.ImpactObserve                                             // 0
}
```

Same tool, different impact per call:

```
bash({"command": "ls -la"})              → impact 0 (observe)
bash({"command": "echo hi > file.txt"})  → impact 1 (affect)
bash({"command": "rm -rf /tmp/data"})    → impact 2 (control)
```

With `intent=1`:
```
bash("ls -la")           → 0 ≤ 1 ✓ runs
bash("echo hi > f.txt")  → 1 ≤ 1 ✓ runs
bash("rm -rf /tmp/data") → 2 > 1 ✗ blocked
```

---

## Example 3: Database Operations

A hypothetical database tool with SQL-aware impact classification:

```go
func (d *Database) Impact(params map[string]any) int {
    query, _ := params["query"].(string)
    upper := strings.ToUpper(query)

    if strings.Contains(upper, "DROP") || strings.Contains(upper, "TRUNCATE") ||
       strings.Contains(upper, "DELETE") {
        return tools.ImpactControl  // 2 — irreversible data loss
    }
    if strings.Contains(upper, "INSERT") || strings.Contains(upper, "UPDATE") ||
       strings.Contains(upper, "ALTER") {
        return tools.ImpactAffect   // 1 — data modification
    }
    return tools.ImpactObserve      // 0 — SELECT, SHOW, DESCRIBE
}
```

```
database({"query": "SELECT * FROM users"})  → impact 0 ✓
database({"query": "UPDATE users SET ..."}) → impact 1 ✓ (at triage)
database({"query": "DROP TABLE users"})     → impact 2 ✗ (blocked at triage)
```

---

## Example 4: Financial Trading

```go
// Market data — read only
func (m *MarketData) Impact(map[string]any) int { return tools.ImpactObserve }

// Place order — side effect (reversible via cancel)
func (o *PlaceOrder) Impact(params map[string]any) int {
    amount, _ := params["amount"].(float64)
    if amount > 100000 {
        return tools.ImpactControl  // large orders require explicit act intent
    }
    return tools.ImpactAffect
}

// Cancel order — side effect
func (c *CancelOrder) Impact(map[string]any) int { return tools.ImpactAffect }
```

Here the **same tool** (`PlaceOrder`) returns different impact levels based on the order size. Small orders pass at triage, large orders require explicit act intent.

---

## Why This Is Different From Approval Prompts

Traditional AI safety uses approval prompts: the LLM asks "Should I do X?" and the human clicks yes/no.

Problems with approval prompts:
1. **Human fatigue** — after clicking "yes" 50 times, you click yes on the 51st without reading
2. **LLM framing** — the model describes the action; it can make dangerous things sound benign
3. **Reactive** — the check happens after the LLM has already decided to act
4. **Binary** — yes or no, no graduated levels

IBE advantages:
1. **Mathematical** — `impact ≤ min(intent, clearance)`, no judgment call
2. **Pre-emptive** — intent is set before the conversation, not during
3. **Graduated** — three levels allow nuanced access control
4. **Parameter-aware** — the same tool can have different impact depending on what it's doing
5. **LLM-proof** — the model can't frame, argue with, or social-engineer a math check
6. **Auditable** — every gate decision is logged with skill, params, intent, impact, and result

---

## Implementation

Tools implement the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Impact(params map[string]any) int
    Execute(ctx context.Context, params map[string]any) (string, error)
}
```

Register with the agent:

```go
reg := agent.Registry()
reg.Replace(NewCamera(), "drone")
reg.Replace(NewEngineStart(), "drone")
reg.Replace(NewWeaponFire(), "drone")
```

Set intent via config or API:

```json
{"agent": {"safety_level": 1}}
```

Or per-request:

```
POST /api/v1/execute
{"query": "...", "intent": "triage"}
```

The gate, scheduler, planner, and audit trail handle everything else automatically.
