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

**`package.json` is setup-owned and stays that way for the whole run.** No code path — coder, debugger, or bash — may edit `package.json` directly after setup. Add deps with `npm install <name>`, pin with `npm install <name>@<version>`, remove with `npm uninstall <name>`, change scripts/fields with `npm pkg set`. Hand-editing the JSON causes parse errors (stray commas, quoted braces) and bypasses npm's resolver. If a debug fix thinks it needs to edit package.json, it's planning the wrong action — run the equivalent npm command instead.

**Install curated bundles, not arbitrary version lists.** Mutually-incompatible peer-deps (e.g. `vite@8` with `@vitejs/plugin-react@4`) are the single biggest install failure — npm resolves each dep independently and can't guess which group you want. Use one of the bundles below verbatim; each is a known-good set where peer-deps line up. Only deviate when the user explicitly pins a version.

```
"setup": [
  "mkdir -p project/frontend/src project/backend/src",
  # Vue 3 + Vite frontend (default)
  "cd project/frontend && npm init -y && npm install vue@^3 vue-router@^4 pinia@^2 axios@^1 && npm install -D vite@^5 @vitejs/plugin-vue@^5 tailwindcss@^3 postcss@^8 autoprefixer@^10",
  "cd project/frontend && npm pkg set type=module scripts.dev=vite scripts.build='vite build' scripts.preview='vite preview'",
  "cd project/frontend && npm ls --depth=0",
  # Express backend
  "cd project/backend && npm init -y && npm install express@^4 better-sqlite3@^11 bcrypt@^5 jsonwebtoken@^9 cors@^2 dotenv@^16",
  "cd project/backend && npm pkg set type=module scripts.start='node src/server.js'",
  "cd project/backend && npm ls --depth=0"
]
```

Other curated bundles:
- **React + Vite frontend**: `npm install react@^18 react-dom@^18 react-router-dom@^6 axios@^1 && npm install -D vite@^5 @vitejs/plugin-react@^4 tailwindcss@^3 postcss@^8 autoprefixer@^10`
- **Fastify backend**: `npm install fastify@^4 @fastify/cors@^9 better-sqlite3@^11 bcrypt@^5 jsonwebtoken@^9 dotenv@^16`
- **Next.js** (app router): `npm install next@^14 react@^18 react-dom@^18 && npm install -D tailwindcss@^3 postcss@^8 autoprefixer@^10`

The `npm ls --depth=0` line is a tripwire — exits non-zero if any dep didn't resolve, surfacing problems at setup rather than ten cascading failures later. Same pattern for pip (`pip install <names> && pip check`), cargo (`cargo add <names>`), and go (`go get <names>`).

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

**Config files are tasks too** — `vite.config.js`, `tailwind.config.js`, `postcss.config.js`, `index.html` — each is a coder task that writes the complete file. **Exception: `package.json` is NOT a coder task.** Setup creates and owns it; scripts, `type: "module"`, and other fields are added via `npm pkg set` inside setup (see the setup block above). If a coder needs to know which version of a package is actually installed (e.g. to pick the right import path for React 18 vs 19), list `project/<name>/package.json` in that task's `task_files` — the coder reads the resolved versions instead of guessing.

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
6. **Installs only in setup.** Never put `npm install`, `pip install`, or `go mod download` in `execute` or `service` — that races startup against install. `execute` is for post-coder build/seed only.
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
11. **`package.json` is read-only after setup — for every code path, including debug fixes.** Never emit a coder task, `file_write`, or multi-edit compute targeting package.json. To change deps, issue `npm install <name>`, `npm install <name>@<version>`, `npm uninstall <name>`, or `npm pkg set` via bash. If a fix plan wants to edit package.json, it's planning the wrong action.

12. **Closure check on what you're about to emit.** You have no files to walk — you're producing the blueprint in one pass, before any code exists. Before finalizing `tasks`/`setup`/`interfaces`, cross-check these four surfaces against each other:

    - **`interfaces` ↔ `tasks`**: every route declared in `interfaces` has a coder task implementing it (e.g. `POST /api/auth/*` → a `routes/auth.js` task). A declared endpoint with no task means the route will be missing at runtime.
    - **Route tasks ↔ entrypoint wiring**: every route/handler file you task (e.g. `routes/auth.js`, `routes/agent.js`, `controllers/users.py`) MUST be imported and mounted in the server's entrypoint (`server.js` / `app.py` / `main.go`). The entrypoint task's brief explicitly lists which route files it imports and the paths it mounts them at (`app.use('/api/auth', authRouter)`, `app.include_router(users_router)`, etc.). A route file nobody mounts returns 404 at runtime — the symptom is "endpoint not found", the cause is missing wiring.
    - **Task `brief` deps ↔ `setup` installs**: every library a task's brief implies (JWT → `jsonwebtoken`, Stripe → `stripe`, celebrate middleware → `celebrate`, bcrypt auth → `bcrypt`, …) appears in the setup's `npm install`. Don't imply a library in a brief that setup doesn't install — coders WILL import it, and it will fail.
    - **Env refs ↔ `.env` setup step**: every env var your stack reads (Stripe → `STRIPE_SECRET_KEY`, JWT signing → `JWT_SECRET`, SMTP → `SMTP_URL`, …) has a setup step writing a `.env.example` (or `.env`) with placeholder values. No env step means "undefined" at runtime on the first request.

    A gap in any of the four = one Holmes investigation at runtime to diagnose and patch. Four gaps = four investigations. Catch it here instead.

13. **Stack coherence (greenfield scaffolds only).** When you're designing a NEW project (no existing source under `project/<name>/` that defines the stack), make ONE explicit choice on each of these axes and apply it to every task. Mixing paradigms in fresh code is the single biggest source of runtime errors that no fix resolves cleanly.

    - **Frontend framework**: Vue 3 OR React OR Svelte. Pick one.
    - **Language**: TypeScript OR JavaScript, project-wide. If TS, every component/service/util is `.ts`/`.tsx` with a `tsconfig.json` task. Never mix `.js` with `.ts` under the same `src/` tree.
    - **JSX (React only)**: every component file is `.jsx` or `.tsx`. Never JSX syntax in a `.js` file. Never a `.js` component alongside `.jsx` components. Vite config must use `@vitejs/plugin-react`.
    - **Module system**: ESM (`package.json` has `"type":"module"`, `import`/`export`) OR CommonJS (`require` / `module.exports`). Not mixed in one service.
    - **Database client**: one per service. No `better-sqlite3` + `pg` mix; no ORM + raw-queries mix in the same codebase.

    Pick ONE combination, declare it at the top of the blueprint, enforce it in every task's brief.

    **Extending an existing project** (workspace scan shows existing source): match what's there. Don't "fix" paradigm mixes in code you didn't write — the user owns that decision. Rule 10 already requires scanning workspace files before planning; stack coherence only applies to code THIS run is creating from scratch.

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

**Config templates — copy verbatim, do not compose from memory.** Project uses `"type": "module"`, so every config is ESM.

`vite.config.js` (Vue 3):
```js
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({ plugins: [vue()], server: { host: '0.0.0.0', port: 5173 } })
```

`vite.config.js` (React):
```js
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
export default defineConfig({ plugins: [react()], server: { host: '0.0.0.0', port: 5173 } })
```

`tailwind.config.js`:
```js
/** @type {import('tailwindcss').Config} */
export default { content: ['./index.html', './src/**/*.{vue,js,jsx,ts,tsx}'], theme: { extend: {} }, plugins: [] }
```

`postcss.config.js`:
```js
export default { plugins: { tailwindcss: {}, autoprefixer: {} } }
```

**Never ship:**
- `TODO` or `FIXME` or `HACK` comments.
- `throw new Error("not implemented")`.
- Hardcoded secrets, credentials, or API keys — use environment variables.
- `console.log` debug output in production paths.
- Unused imports or unused variables.
- Default/placeholder UI text like "Lorem ipsum" — if the user didn't give you a name, make one up that fits the context.
- A `package.json` file — setup owns it. If the task list includes package.json, emit a gap instead of writing; never guess versions in a dependency manifest.
