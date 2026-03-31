---
name: client_intake
description: Create a new client intake folder with standard template documents
impact: 1
parameters:
  client_name: { type: string, description: "Full client name" }
  matter_type: { type: string, description: "Type of matter (litigation, contract, estate, corporate)" }
  contact_email: { type: string, description: "Client email address" }
---

Create a new client intake folder and populate it with template documents.

1. Create folder: cases/{{client_name}}/
2. Create file: cases/{{client_name}}/intake.md with:
   - Client: {{client_name}}
   - Matter type: {{matter_type}}
   - Contact: {{contact_email}}
   - Intake date: (today's date)
   - Status: New
3. Create file: cases/{{client_name}}/notes.md with an empty notes template
4. Create file: cases/{{client_name}}/billing.csv with header row: Date,Hours,Description,Rate

Report back what was created.
