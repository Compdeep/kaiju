---
name: timesheet_entry
description: Log billable hours for a client matter
impact: 1
parameters:
  client: { type: string, description: "Client name" }
  matter: { type: string, description: "Matter or case reference" }
  hours: { type: number, description: "Hours worked (e.g. 1.5)" }
  description: { type: string, description: "Work performed" }
  date: { type: string, description: "Date (YYYY-MM-DD, default today)" }
---

Create a timesheet entry in CSV format and append it to the timesheet file.

Format: {{date}},{{client}},{{matter}},{{hours}},{{description}}

Append this line to the file: timesheets/{{client}}-{{matter}}.csv

If the file doesn't exist yet, add a header row first:
Date,Client,Matter,Hours,Description
