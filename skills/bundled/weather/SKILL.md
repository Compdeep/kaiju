---
name: weather
description: "Get current weather and forecasts via wttr.in. Use when the user asks about weather, temperature, or forecasts. No API key needed."
---

## When to Use

Use when the user asks about weather, temperature, or forecasts for any location.

Do NOT use for historical weather data, severe weather alerts, or detailed meteorological analysis.

## Planning Guidance

### Current weather

Single step:

1. `bash` — `curl -s "wttr.in/London?format=4"` (replace city)

### Detailed forecast

1. `bash` — `curl -s "wttr.in/London?format=v2"`

### Multiple locations

Plan in parallel:

1. `bash` — `curl -s "wttr.in/London?format=4"`
2. `bash` — `curl -s "wttr.in/Tokyo?format=4"` (parallel with step 0)
3. `bash` — `curl -s "wttr.in/NewYork?format=4"` (parallel with step 0)

### Format options

- `?format=4` — one-line: city, condition, temp, wind
- `?format=v2` — detailed 3-day forecast with graphics
- `?format=j1` — JSON output (for programmatic use)
- `?format=%C+%t+%w` — custom: condition, temp, wind

### What NOT to do

- Don't use `web_search` for weather — wttr.in is faster and needs no API key
- Don't plan sequential weather lookups for independent cities — parallelize them
