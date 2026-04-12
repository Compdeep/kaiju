# Run Report: 128 Node Run (2026-04-08 18:21-18:26)

## Totals
- **Total nodes**: 127
- **LLM calls**: 50 (budget ceiling)
- **Duration**: 4m51s
- **Outcome**: Failed (backend still broken)

## Breakdown
| Category | Count | % |
|----------|-------|---|
| Resolved OK | 27 | 21% |
| Failed | 31 | 24% |
| Micro-planner spawns | 31 | 24% |
| Retry proxies | 18 | 14% |
| Reflections | 10 | 8% |
| Other infra | 10 | 8% |

## Waste Analysis
- **Validator micro-planner calls**: 21 of 31 (68% of ALL micro-planner)
  - revalidate_Backend_health: 6 calls
  - revalidate_Auth_register: 6 calls
  - verify_backend_health: 3 calls
  - verify_Backend_health: 2 calls
  - verify_Auth_register: 2 calls
  - check_backend_health: 2 calls
- **Health check nodes**: 34 executed, 26 failed (76% failure rate)
- **Useful work**: ~40 nodes (31%)
- **Validator waste**: ~60 nodes (47%)
- **Other waste**: ~27 nodes (22%)

## Root Cause
Backend package.json had trailing comma -> npm install fails -> service can't start -> all validators fail -> micro-planner retries each validator -> burns budget -> reflector never gets enough replans to fix the actual file.
