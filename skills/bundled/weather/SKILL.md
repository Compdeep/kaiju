---
name: weather
description: "Get current weather and forecasts via wttr.in (today and the next 2 days) or Open-Meteo (up to 16 days). Use when the user asks about weather, temperature, or forecasts. No API key needed."
---

## When to Use

Use when the user asks about weather, temperature, or forecasts for any location.

Do NOT use for historical weather data, severe weather alerts, or detailed meteorological analysis.

## Planning Guidance

### Current weather

Single step:

1. `bash` — `curl -s "wttr.in/London?format=4"` (replace city)

### Detailed forecast (today and the next 2 days)

1. `bash` — `curl -s "wttr.in/London?format=v2"`

wttr.in covers today and the next 2 days only. No format gives more.

### No location given

When the user names no place and none is known from the conversation or memory, don't ask for one. Leave the city out and wttr.in uses the location of the machine making the request:

1. `bash` — `curl -s "wttr.in/?format=4"`

The reply starts with the place it chose. Name that place in the answer, so the user can give another if it is wrong.

### A day more than 2 days ahead

Use Open-Meteo, which covers up to 16 days. Two steps, the second taking the coordinates from the first:

1. `bash` — `curl -s "https://geocoding-api.open-meteo.com/v1/search?name=Tokyo&count=1"` (replace city; the reply has `latitude` and `longitude`)
2. `bash` — `curl -s "https://api.open-meteo.com/v1/forecast?latitude=35.69&longitude=139.69&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,wind_speed_10m_max&timezone=auto&forecast_days=7"`

The reply lists one value per day under `daily`, in the order of `daily.time`. `weather_code` is a WMO code: 0 clear, 1-3 partly cloudy to overcast, 45-48 fog, 51-67 drizzle and rain, 71-77 snow, 80-82 showers, 95-99 thunderstorm.

### Multiple locations

Plan in parallel:

1. `bash` — `curl -s "wttr.in/London?format=4"`
2. `bash` — `curl -s "wttr.in/Tokyo?format=4"`
3. `bash` — `curl -s "wttr.in/NewYork?format=4"`

Nothing links these three, so they run at the same time. Give each a tag and leave `depends_on` empty.

### Format options

- `?format=4` — one-line: city, condition, temp, wind
- `?format=v2` — detailed 3-day forecast with graphics
- `?format=j1` — JSON output (for programmatic use)
- `?format=%C+%t+%w` — custom: condition, temp, wind

Any other format value is printed back as plain text (`format=w1` returns `w1`), which is not weather data.

### What NOT to do

- Don't use `web_search` for weather while wttr.in or Open-Meteo covers the day asked about — both are faster and need no API key
- Don't plan sequential weather lookups for independent cities — parallelize them
