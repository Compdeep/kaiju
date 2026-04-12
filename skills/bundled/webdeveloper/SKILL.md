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

Do NOT use for:
- pure data analysis or ML scripts (use `data_science`)
- CLI tools and scripts
- infrastructure and devops
- system programming

## Planning Guidance

Web projects are multi-file and layered. A "simple login app" is not one file — it is models, migrations, middleware, routes, views, components, an API client, and error handling.

When the user asks for a webapp:

1. **Always use `compute` with `mode:"deep"` and `skill:"webdeveloper"`** — the architect needs room to decompose into many coherent files. A shallow compute producing one giant file is the wrong shape for web work.
2. **Pass the full goal with context** — tech stack if specified ("Vue 3 + Express + Postgres"), features ("login, dashboard, user list"), and any design direction ("dark mode, minimal, tailwind").
3. **Do not follow compute with a bunch of bash steps to "make it work"** — the architect emits `setup` commands and the scheduler grafts them automatically. Trust the compute pipeline.

## Architect Guidance

Decompose webapps the way a real engineering team would: deep, layered, with clear ownership boundaries. Not three files. A real webapp is 10–25 files.

**The blueprint is the SOLE source of truth.** Every file path, every directory, every config — defined in the blueprint. Coders write exactly what the blueprint says. Nothing else decides the project structure.

**No scaffolders.** Do NOT use `npx create-next-app`, `npm create vite`, `create-react-app`, or any scaffolding tool. They produce unpredictable directory structures that conflict with the blueprint. Instead, create all files explicitly through tasks — `package.json`, `tsconfig.json`, config files, entry points — everything. The blueprint owns the structure.

**Default frontend: Vue 3 + Vite.** Unless the user asks for React, Next.js, Svelte, etc., always use Vue 3 with `<script setup>`, Vite, and Tailwind CSS. Vue is lightweight, fast, and the coders handle it reliably. No Server Component confusion, no `'use client'` directives.

**Setup commands** are for `mkdir -p` and `npm install` only:
```
"setup": [
  "mkdir -p project/frontend/src/views project/frontend/src/components project/frontend/src/stores project/frontend/src/api project/frontend/src/router project/frontend/public",
  "mkdir -p project/backend/src/routes project/backend/src/middleware project/backend/src/models project/backend/db",
  "cd project/frontend && npm init -y && npm install vue vue-router pinia axios",
  "cd project/frontend && npm install -D vite @vitejs/plugin-vue tailwindcss postcss autoprefixer",
  "cd project/backend && npm init -y && npm install express better-sqlite3 bcrypt jsonwebtoken cors dotenv"
]
```

**Separate frontend and backend cleanly.** Typical layout for a full-stack app:
```
project/
  frontend/
    src/
      views/             — page-level components (Home.vue, Login.vue, Dashboard.vue)
      components/        — reusable UI pieces (HeroSection.vue, ThemeToggle.vue)
      stores/            — Pinia stores (auth.js, user.js)
      api/               — HTTP client, endpoint wrappers (auth.js, client.js)
      router/            — route definitions (index.js)
      App.vue            — root component
      main.js            — entry point
      style.css          — global styles + Tailwind directives
    index.html           — Vite entry HTML
    package.json
    vite.config.js
    tailwind.config.js
    postcss.config.js
  backend/
    src/
      routes/            — one file per resource
      middleware/         — auth, logging, error, CORS
      models/            — data access, one per entity
      server.js          — entry point
    db/
      seed.js            — database init + seed
    package.json
```

**Design the interfaces before decomposing.** Use the `interfaces` field to lock the REST API contract so frontend and backend coders produce compatible code:
```
"interfaces": {
  "POST /api/auth/register": {"request": {"email": "string", "password": "string", "name": "string"}, "response": {"token": "string", "user": {"id": "number", "email": "string", "name": "string"}}},
  "POST /api/auth/login":    {"request": {"email": "string", "password": "string"}, "response": {"token": "string", "user": {"id": "number", "email": "string"}}},
  "GET /api/users/me":       {"headers": {"Authorization": "Bearer <token>"}, "response": {"id": "number", "email": "string", "name": "string"}}
}
```

**Design the database schema up front.** Use the `schema` field. Default to SQLite unless the user asked for something specific:
```
"schema": {
  "type": "sqlite",
  "tables": {
    "users": "id integer primary key autoincrement, email text unique not null, password_hash text not null, name text not null, created_at text default (datetime('now'))"
  }
}
```

**One file per task.** Each task owns exactly one file. Large webapps become many tasks. Don't group `server.js + auth.js + routes.js` into one task — split them.

**Config files are tasks too.** `package.json`, `vite.config.js`, `tailwind.config.js`, `postcss.config.js`, `index.html` — each is a task that writes the complete file. The setup command does `npm init -y` to create a skeleton, then a coder task overwrites `package.json` with the correct scripts and config. The `package.json` MUST include scripts: `{"dev": "vite", "build": "vite build", "preview": "vite preview"}`.

**Execute and service fields.** For the final task that starts the dev servers, use `service`:
```
{"service": {"command": "node project/backend/src/server.js", "name": "backend"}}
{"service": {"command": "npm run dev --prefix project/frontend", "name": "frontend"}}
```

## RULES — Architect MUST follow these

1. **Every webapp needs TWO services — backend AND frontend. Both MUST have a `service` field.** Without both, validators that hit either server will fail.
2. **NO SCAFFOLDERS.** Never use `npx create-next-app`, `npm create vite`, `create-react-app`, `django-admin startproject`, or any scaffolding tool. Create every file through blueprint tasks. The blueprint is the sole authority on project structure.
3. **Tasks that produce SQL migration/seed files MUST include an `execute` field** to apply them (e.g. `"execute": "node project/backend/db/seed.js"`).
4. **Never rely on validation checks to start services.** Validation checks only verify. Services must be declared in the task's `service` field.
5. **Setup commands run BEFORE coders. Execute/service commands run AFTER coders. Validators run AFTER execute/service.** Design your tasks knowing this order.
6. **Execute fields must install dependencies before building.** Frontend execute: `"cd project/frontend && npm install && npm run build"`. Never just `npm run build` alone.
7. **Setup commands must be non-interactive.** Always pass `--yes`, `-y` flags.
8. **Default to SQLite unless the user specifies a different database.**
9. **ALWAYS emit validation checks for every service.** The `check` field MUST be a shell command (curl, node -e, test). NEVER prose like "Manual test". Use ACTUAL ports from your services. Example:
```json
"validation": [
  {"name": "backend health", "check": "curl -sf http://localhost:4000/health", "expect": "returns 200"},
  {"name": "frontend loads", "check": "curl -sf http://localhost:3000/", "expect": "returns 200 with HTML"},
  {"name": "auth register", "check": "curl -sf -X POST http://localhost:4000/auth/register -H 'Content-Type: application/json' -d '{\"email\":\"test@test.com\",\"password\":\"pass123\",\"name\":\"Test\"}'", "expect": "returns token"}
]
```
2-5 checks total. Every check must be a command that exits 0 on success.
10. **If the workspace already has files, scan them first.** Check existing `package.json`, directory structure, and config before planning. Write the blueprint around what exists — extend, don't overwrite.

## Debug Guidance

When diagnosing web app failures, follow this procedure — don't guess, read the evidence:

**Service not responding (curl connection refused):**
1. Read the service error log FIRST: use service(action="logs", name="<service>", lines=30)
2. The log tells you WHY. Common causes:
   - "Missing script: dev" → package.json has no dev script. Fix with file_write.
   - "MODULE_NOT_FOUND: <pkg>" → dependency not installed. Run npm install <pkg>.
   - "EADDRINUSE" → port already in use. Another process is on that port.
   - "Cannot find module './routes/auth'" → file path mismatch. Check the import vs actual file path.
3. Fix the ROOT CAUSE with compute (code fix) or file_write (config fix), then restart the service.
4. NEVER retry curl when the service error log shows a startup failure. Fix the failure first.

**Build fails (vite build, npm run build):**
1. The error names the file and the problem. Fix THAT FILE with a compute node.
2. Common: missing import, wrong export, missing dependency, TypeScript type error.
3. Don't retry the build. Fix the code, then rebuild.

**npm install fails:**
1. Check if package.json exists and has correct format.
2. "ENOENT package.json" → the directory wasn't set up. Check setup steps.
3. Network errors → retry once, then skip.

**package.json issues:**
1. Must have "scripts": {"dev": "vite", "build": "vite build"} for Vue/Vite projects.
2. Must list all dependencies the code imports.
3. If missing scripts or deps, overwrite package.json with file_write — don't patch, rewrite the whole file.

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
- Hash passwords with bcrypt (cost >= 10) or argon2 — never store plaintext, never use MD5/SHA1.
- Return plain objects, not database driver rows — translate at the model boundary.
- Handle not-found as a return value (null or undefined), not an exception — let the caller decide how to respond.

**API clients (frontend):**
- One function per endpoint. Strong types if TS.
- Attach auth token from the store automatically.
- Handle network errors (timeout, offline) and HTTP errors (4xx, 5xx) — return structured results or throw typed errors.

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
- Keep imports at the top, sorted logically (framework, library, local).
- Export the main thing first.
- No commented-out code, no `console.log` left behind, no dead branches.
- Comments only where the logic needs them, not for obvious code.

**Never ship:**
- `TODO` or `FIXME` or `HACK` comments.
- `throw new Error("not implemented")`.
- Hardcoded secrets, credentials, or API keys — use environment variables.
- `console.log` debug output in production paths.
- Unused imports or unused variables.
- Default/placeholder UI text like "Lorem ipsum" — if the user didn't give you a name, make one up that fits the context.
