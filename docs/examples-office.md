# Office Example — Law Firm Without Engineers

## The Problem

A small law firm wants an AI assistant for daily operations. Nobody on staff writes code. They need:
- File management across case folders
- Time tracking and billing
- Legal research
- Client intake workflows
- Court deadline calculations
- Protection against accidental data loss

## Setup (No Code Required)

### 1. Install kaiju

Copy the binary. No dependencies, no Node.js, no Docker.

```bash
./kaiju chat
```

### 2. Set the API key

```bash
export OPENAI_API_KEY=sk-...
```

### 3. Configure safety level

Create `kaiju.json`:

```json
{
  "llm": {
    "api_key": "${OPENAI_API_KEY}",
    "model": "gpt-4o"
  },
  "agent": {
    "safety_level": 1,
    "dag_mode": "nReflect"
  },
  "skills_dirs": ["./skills"]
}
```

`safety_level: 1` (triage) means:
- **Can** read files, search the web, list processes, check disk space
- **Can** write files, create folders, append to CSVs, create archives
- **Cannot** delete files, run destructive commands, push to git, kill processes

This is set in config, not by the LLM. The LLM cannot escalate it.

### 4. Drop in SKILL.md files

The office manager creates plain markdown files in `./skills/`. No Go, no TypeScript, no compilation.

```
skills/
  timesheet/SKILL.md       — log billable hours
  client-intake/SKILL.md   — create new client folders
  court-deadline/SKILL.md  — calculate filing deadlines
```

These are hot-reloaded — edit the file, kaiju picks it up within 30 seconds.

## Daily Usage

### Morning: Check what needs attention

```
Lawyer: "what cases have files modified this week?"

Kaiju plans (DAG, parallel):
  n1: bash({"command": "find cases/ -mtime -7 -type f"})  → impact 0 ✓
  n2: sysinfo({})                                          → impact 0 ✓

Aggregator: "3 cases had activity this week:
  - Richardson v. Smith: retainer.pdf updated Tuesday
  - Kemp Estate: will-draft-v3.docx updated yesterday
  - Ortega Corp: contract-review.md updated today"
```

### New client walks in

```
Lawyer: "new client intake for Sarah Chen, employment litigation, sarah@chen.com"

Kaiju plans:
  n1: client_intake({"client_name": "Sarah Chen",
                      "matter_type": "litigation",
                      "contact_email": "sarah@chen.com"})  → impact 1 ✓

Result: creates cases/Sarah Chen/ with intake.md, notes.md, billing.csv
```

### Research a legal question

```
Lawyer: "what's the statute of limitations for wrongful termination in California?"

Kaiju plans (DAG, parallel):
  n1: web_search({"query": "California statute of limitations wrongful termination"})
  n2: web_search({"query": "Cal. Code Civ. Proc. §340 employment claims"})

Aggregator: "In California, the statute of limitations for wrongful termination is:
  - 2 years for most claims (CCP §335.1)
  - 3 years if based on a written contract (CCP §337)
  - 1 year for FEHA discrimination claims (Gov. Code §12960)
  Note: These deadlines can be tolled. Verify with current case law."
```

### Calculate a deadline

```
Lawyer: "when is the response deadline for the Richardson complaint served June 15?"

Kaiju plans:
  n1: court_deadline({"event_date": "2026-06-15",
                       "jurisdiction": "Federal 5th Circuit",
                       "deadline_type": "response"})  → impact 0 ✓

Result: "Response due: July 7, 2026 (Monday). 21 days from service.
  July 6 falls on Sunday, moved to next business day.
  106 calendar days remaining from today."
```

### Log time

```
Lawyer: "log 2.5 hours on Richardson, drafted motion to dismiss"

Kaiju plans:
  n1: timesheet_entry({"client": "Richardson",
                        "matter": "v-Smith",
                        "hours": 2.5,
                        "description": "Drafted motion to dismiss"})  → impact 1 ✓

Result: "Logged 2.5 hours to timesheets/Richardson-v-Smith.csv"
```

### End of month: Package case files

```
Lawyer: "zip up the Richardson case for opposing counsel's document request"

Kaiju plans:
  n1: file_list({"path": "cases/Richardson"})           → impact 0 ✓
  n2: archive({"action": "create",
               "archive_path": "Richardson-disclosure.zip",
               "files": ["cases/Richardson/"]})          → impact 1 ✓
  (n2 depends on n1)

Result: "Created Richardson-disclosure.zip with 14 files (2.3 MB)"
```

## What the IBE Gate Catches

### Accidental deletion

```
Lawyer: "clean up the old 2019 cases, we don't need them anymore"

Planner generates: bash({"command": "rm -rf cases/2019/"})

Gate: impact = 2 (destructive pattern matched: rm -rf)
      effective = min(intent=1, clearance=1) = 1
      2 > 1 → BLOCKED

Kaiju responds: "I can't delete files at the current safety level.
  To remove the 2019 cases, you'd need to set safety_level to 2 (act),
  or delete them manually. Would you like me to list what's in that
  folder first so you can review?"
```

### LLM hallucination

```
Lawyer: "send the Richardson file to opposing counsel"

LLM hallucinates a bash command with curl to upload the file somewhere.

Gate: bash({"command": "curl -X POST ... -F file=@..."})
      impact = 1 (write pattern: curl -o detected... actually this is
      an outbound POST, but the pattern catches it as a write operation)

Even if it passes at triage, the file goes nowhere because we don't have
an email tool registered. The planner would fail to find a "send email"
skill and the aggregator would report: "I don't have the ability to send
emails directly. You could attach Richardson-disclosure.zip to an email
manually, or we can set up an email skill."
```

### Protecting client confidentiality

```
Lawyer: "push all our case files to the shared repository"

Planner generates: git({"action": "push"})

Gate: impact = 2 (push is control/destructive)
      2 > 1 → BLOCKED

This prevents accidental publication of confidential case files.
```

## How SKILL.md Files Work

A SKILL.md file is just markdown with YAML frontmatter:

```yaml
---
name: timesheet_entry
description: Log billable hours for a client matter
impact: 1
parameters:
  client: { type: string, description: "Client name" }
  hours: { type: number, description: "Hours worked" }
  description: { type: string, description: "Work performed" }
---

(Instructions for the LLM on how to execute this skill)
```

The `skillmd` loader reads these, registers them in the same tool registry as compiled tools, and they appear to the planner identically. The `impact` field in frontmatter sets the IBE level.

**No compilation. No deployment. Edit the markdown, kaiju picks it up.**

The office manager can create new workflows by writing plain English instructions in a markdown file. If they want a "generate invoice" skill, they write a SKILL.md that describes the invoice format and which tools to use (file_read to get billing data, file_write to create the invoice). The planner chains it all together.

## Safety Summary

| Action | Impact | Allowed at safety_level 1? |
|--------|--------|---------------------------|
| Read any file | 0 | ✓ |
| Search the web | 0 | ✓ |
| List files/processes | 0 | ✓ |
| Check disk space | 0 | ✓ |
| Calculate deadlines | 0 | ✓ |
| Write/create files | 1 | ✓ |
| Create archives | 1 | ✓ |
| Log timesheet entries | 1 | ✓ |
| Append to CSVs | 1 | ✓ |
| Delete files | 2 | ✗ Blocked |
| Push to git | 2 | ✗ Blocked |
| Kill processes | 2 | ✗ Blocked |
| Run rm/del commands | 2 | ✗ Blocked |

The firm gets a capable assistant that can do real work but structurally cannot destroy data, publish confidential files, or run destructive operations — regardless of what the LLM tries to do.
