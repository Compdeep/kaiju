---
name: webdeveloper
description: "Full-stack web application development — frontend, backend, APIs, databases, auth, UI/UX. Use when building webapps, websites, SPAs, REST or GraphQL APIs, or anything with a browser-facing interface."
---

## When to Use

Use when the goal involves any of:
- building a web application (SPA, MPA, static site, dashboard)
- writing an HTTP server or REST/GraphQL API
- creating UI components, forms, pages, or layouts
- wiring a frontend to a backend
- user authentication, sessions, JWT, OAuth
- database-backed web apps (Postgres, MySQL, SQLite, Mongo)
- scaffolding a project with Vite, Next, Nuxt, Vue, React, Svelte, Express, Fastify, FastAPI, Django, Rails, etc.

Do NOT use for:
- pure data analysis or ML scripts (use `data_science`)
- CLI tools and scripts
- infrastructure and devops
- system programming

## Planning Guidance

Web projects are multi-file and layered. A "simple login app" is not one file — it is models, migrations, middleware, routes, views, components, an API client, and error handling, plus scaffolding and dev server setup.

When the user asks for a webapp:

1. **Always use `compute` with `mode:"deep"` and `skill:"webdeveloper"`** — the architect needs room to decompose into many coherent files. A shallow compute producing one giant file is the wrong shape for web work.
2. **Pass the full goal with context** — tech stack if specified ("Vue 3 + Express + Postgres"), features ("login, dashboard, user list"), and any design direction ("dark mode, minimal, tailwind").
3. **Do not follow compute with a bunch of bash steps to "make it work"** — the architect emits `setup` commands (scaffold + install) and the scheduler grafts them automatically. Trust the compute pipeline.
4. **If the user asks to see or run the result**, add a final step after compute to launch the dev server or open the URL — but the compute node's `service` field handles long-running processes, so often no extra step is needed.

## Architect Guidance

Decompose webapps the way a real engineering team would: deep, layered, with clear ownership boundaries. Not three files. A real webapp is 10–25 files.

**Scaffolding first.** Always begin with a proper project scaffold via the `setup` array. Use the framework's official tool — `npm create vite@latest`, `npx create-next-app`, `django-admin startproject`, `rails new`, `npm init -y && npm install express`, etc. Never manually `mkdir` and write config files by hand when a scaffolder exists. The scaffolder produces correct `package.json`, `tsconfig`, `vite.config`, and build wiring that would take 10 tasks to recreate by hand and still be wrong.

**Separate frontend and backend cleanly.** Typical layout for a full-stack app:
```
project/
  frontend/          (Vue/React/Svelte + build tool)
    src/
      components/    (reusable UI pieces — Button, Input, Card)
      views/ or pages/  (top-level routes — Login, Dashboard, Profile)
      stores/        (state — Pinia, Redux, Zustand, Svelte stores)
      api/           (HTTP client, endpoint wrappers)
      router/        (route definitions)
      styles/        (global CSS, tailwind config, theme)
      main.js/ts     (entry point)
  backend/           (Express/Fastify/FastAPI/Django)
    src/
      routes/        (one file per resource — auth, users, posts)
      middleware/    (auth, logging, error, CORS, rate limit)
      models/        (data access — one file per entity)
      services/      (business logic, called by routes)
      db/            (connection, migrations, seeds)
      server.js/ts   (entry point)
  docker-compose.yml (if using Docker for dev)
  .env.example
  README.md
```

**Design the interfaces before decomposing.** Use the `interfaces` field to lock the REST API contract so frontend and backend coders produce compatible code:
```
"interfaces": {
  "POST /api/auth/register": {"request": {"email": "string", "password": "string", "name": "string"}, "response": {"token": "string", "user": {"id": "number", "email": "string", "name": "string"}}},
  "POST /api/auth/login":    {"request": {"email": "string", "password": "string"}, "response": {"token": "string", "user": {"id": "number", "email": "string"}}},
  "GET /api/users/me":       {"headers": {"Authorization": "Bearer <token>"}, "response": {"id": "number", "email": "string", "name": "string"}}
}
```
Frontend coders read this to build their API client; backend coders read it to implement the routes. Both see the exact same contract — no drift.

**Design the database schema up front.** Use the `schema` field with typed columns. Default to SQLite unless the user asked for something specific:
```
"schema": {
  "type": "sqlite",
  "tables": {
    "users": "id integer primary key autoincrement, email text unique not null, password_hash text not null, name text not null, created_at text default (datetime('now'))",
    "sessions": "id integer primary key autoincrement, user_id integer references users(id), token text unique not null, expires_at text not null"
  }
}
```
The schema is the source of truth for model coders and migration coders alike.

**One file per task.** Each task owns exactly one file. Large webapps become many tasks. Don't group `server.js + auth.js + routes.js` into one task "create backend" — that's a recipe for shallow work. Split them.

**Task decomposition pattern for a typical full-stack login app:**
1. db/migrations/001_initial.sql (schema)
2. backend/src/db/pool.js (database connection)
3. backend/src/models/user.js (user CRUD + password hashing with bcrypt)
4. backend/src/middleware/auth.js (JWT verify middleware)
5. backend/src/middleware/error.js (error handler)
6. backend/src/routes/auth.js (register + login routes, input validation)
7. backend/src/routes/users.js (protected /me endpoint)
8. backend/src/server.js (Express setup, middleware chain, route mounting)
9. frontend/src/api/client.js (fetch wrapper with auth header)
10. frontend/src/api/auth.js (login, register, logout API calls)
11. frontend/src/stores/auth.js (user state, token persistence)
12. frontend/src/router/index.js (route definitions, auth guards)
13. frontend/src/views/Login.vue (login form with validation + error + loading states)
14. frontend/src/views/Register.vue (same pattern)
15. frontend/src/views/Dashboard.vue (protected page showing user info)
16. frontend/src/components/AppLayout.vue (shell with nav + main)
17. frontend/src/App.vue (root)
18. frontend/src/main.js (Vue app entry, plugin registration)

That's 18 tasks for a "simple login app". A "simple" webapp is not three files.

**Setup commands.** Include everything needed to get from empty directory to runnable:
```
"setup": [
  "mkdir -p project && cd project && npm create vite@latest frontend -- --template vue",
  "cd project/frontend && npm install && npm install pinia vue-router axios",
  "mkdir -p project/backend && cd project/backend && npm init -y && npm install express better-sqlite3 bcrypt jsonwebtoken cors dotenv"
]
```

**Execute and service fields.** For the final task that starts the dev servers, use `service`:
```
{"service": {"command": "cd project/backend && node src/server.js", "name": "api-backend"}}
{"service": {"command": "cd project/frontend && npm run dev", "name": "frontend-dev"}}
```

## RULES — Architect MUST follow these

1. **Every webapp needs TWO services — backend AND frontend. Both MUST have a `service` field.** Backend: `{"command": "node project/backend/src/server.js", "name": "backend"}`. Frontend: `{"command": "npm run dev --prefix project/frontend", "name": "frontend"}`. Without both, validators that hit either server will fail.
2. **Don't fight the scaffolder.** If create-next-app creates `app/page.tsx`, write to `app/`, not `pages/`. If Vite creates `src/App.vue`, write to `src/`. Read what the scaffold produced and follow its conventions — don't invent a parallel structure.
3. **Tasks that produce SQL migration/seed files MUST include an `execute` field** to apply them (e.g. `"execute": "node db/seed.js"` for SQLite, or the appropriate CLI for the chosen DB).
4. **Never rely on validation checks to start services.** Validation checks only verify — they don't set up. If a service isn't declared in the task's `service` field, it won't be running when validators fire.
5. **Setup commands run BEFORE coders. Execute/service commands run AFTER coders. Validators run AFTER execute/service.** Design your tasks knowing this order. Don't put server start commands in setup — they'll run before the code exists.
6. **Execute fields must install dependencies before building.** Coders may import packages not in the original setup. Frontend execute: `"cd project/frontend && npm install && npm run build"`. Backend execute: `"cd project/backend && npm install"`. Never just `npm run build` alone — always `npm install &&` first.
7. **Setup commands must be non-interactive.** Always pass flags that skip prompts: `--yes`, `-y`, `--no-input`, `--defaults`. Example: `npx create-next-app@latest frontend --yes`, not `npx create-next-app frontend`. The agent cannot answer interactive prompts.
8. **Default to SQLite unless the user specifies a different database.** SQLite needs zero setup — no install, no auth, no docker. If the user asks for Postgres, MySQL, or Mongo, use that instead. But when the user says "database" without specifying, use SQLite with better-sqlite3 (Node) or sqlite3 (Python).

## Coder Guidance

Every file you write is production code. No stubs, no TODOs, no skeleton functions. Match the depth the file's purpose calls for.

**UI components (Vue / React / Svelte):**
- Full state handling: loading, error, empty, success, disabled.
- Form inputs have labels, placeholders, validation, error display, and disabled state during submission.
- Buttons have hover, active, focus, disabled states (with visible feedback).
- Handle the empty state — what does the dashboard look like with no data? Don't leave it blank.
- Handle the error state — what does the login form show when auth fails? A clear message, not a silent reset.
- Handle the loading state — spinners, skeletons, or disabled inputs during fetch.
- Use semantic HTML — `<button>`, `<form>`, `<label>`, `<nav>`, `<main>` — not `<div>` for everything.
- Accessibility: `aria-label` on icon-only buttons, `aria-invalid` on bad inputs, keyboard focus visible, alt text on images, semantic landmarks.
- Responsive by default — mobile-first, breakpoints for tablet/desktop. Use flex/grid, not fixed widths.
- Use the framework idioms — composables in Vue 3, hooks in React, reactive stores in Svelte. Don't port patterns from other frameworks.

**Forms specifically:**
- Every field has client-side validation before submit (required, format, length).
- Submit button is disabled while the request is in flight AND when validation fails.
- Error messages appear inline next to the field, not as a generic toast.
- On success, give clear feedback — navigate, show a success message, or update the UI.
- Prevent double submission. Don't clear the form on error (user loses their input).

**Backend routes:**
- Validate input on arrival. Return 400 with a specific message on bad input — don't let bad data reach the database.
- Return structured errors: `{"error": "message", "code": "ERR_CODE"}` — not plain text, not HTML.
- Use proper HTTP status codes: 200 for success, 201 for create, 400 for bad input, 401 for unauthenticated, 403 for unauthorized, 404 for not found, 500 for server errors.
- Handle async errors — catch and return, don't let the process crash.
- Auth middleware on protected routes. Never trust the client to include a valid token.
- Never return password hashes, secrets, or internal IDs the client doesn't need.

**Database models:**
- Parameterized queries always. Never string-concatenate SQL.
- Hash passwords with bcrypt (cost ≥ 10) or argon2 — never store plaintext, never use MD5/SHA1.
- Return plain objects, not database driver rows — translate at the model boundary.
- Handle not-found as a return value (null or undefined), not an exception — let the caller decide how to respond.

**API clients (frontend):**
- One function per endpoint. Strong types if TS.
- Attach auth token from the store automatically.
- Handle network errors (timeout, offline) and HTTP errors (4xx, 5xx) — return structured results or throw typed errors.
- Retry idempotent requests on transient failures if appropriate.

**State management:**
- Persist auth token to localStorage or a cookie so it survives reload.
- Never store passwords in state.
- Clear sensitive state on logout.

**CSS and styling:**
- Pick ONE approach: Tailwind, CSS modules, or scoped component styles. Don't mix.
- Consistent spacing scale (4/8/16/24/32 px or rem equivalents).
- Consistent color palette — primary, secondary, accent, neutral, success, warning, error. Don't use random hex values per component.
- Dark mode if the user asked, or at least structured for it (CSS variables).
- Readable typography — 16px minimum body, 1.5 line-height.

**File shape:**
- Keep imports at the top, sorted logically (framework → library → local).
- Export the main thing first.
- No commented-out code, no `console.log` left behind, no dead branches.
- Comments only where the logic needs them, not for obvious code.

**Never ship:**
- `TODO` or `FIXME` or `HACK` comments.
- `throw new Error("not implemented")`.
- Hardcoded secrets, credentials, or API keys — use environment variables.
- `console.log` debug output in production paths.
- Unused imports or unused variables.
- Default/placeholder UI text like "Lorem ipsum" or "Your App Name" — if the user didn't give you a name, make one up that fits the context.
