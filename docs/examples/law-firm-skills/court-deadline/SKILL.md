---
name: court_deadline
description: Calculate court filing deadlines based on jurisdiction rules
impact: 0
parameters:
  event_date: { type: string, description: "Date of triggering event (YYYY-MM-DD)" }
  jurisdiction: { type: string, description: "Jurisdiction (e.g. Texas State, Federal 5th Circuit)" }
  deadline_type: { type: string, description: "Type: response, appeal, discovery, motion" }
---

Calculate the filing deadline based on these rules:

Federal courts:
- Response to complaint: 21 days from service
- Appeal notice: 30 days from judgment
- Discovery responses: 30 days from service
- Motion responses: 14 days from service

Texas State courts:
- Response to complaint (answer): 20 days + next Monday rule
- Appeal notice: 30 days from judgment
- Discovery responses: 30 days from service
- Motion responses: 21 days from service

Starting from {{event_date}} in {{jurisdiction}} for deadline type {{deadline_type}}:
1. Calculate the calendar date
2. Check if it falls on a weekend or federal holiday — if so, move to next business day
3. Report the deadline date and how many calendar days remain from today
