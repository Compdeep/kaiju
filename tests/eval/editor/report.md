# Editor Eval Report

Run: 2026-04-21T21:25:31Z

Summary: 56 total (pass=50, edit_fail=1, check_fail=5)

| Fixture | Query | Status | Reason | ms |
|---|---|---|---|--:|
| tests/eval/editor/corpus/configs/Dockerfile | add a HEALTHCHECK instruction with --interval=30s and --timeout=3s that runs `cu… | check_fail | exit status 1 —  | 2249 |
| tests/eval/editor/corpus/configs/Dockerfile | convert this to a multi-stage build. First stage `FROM node:20-alpine AS builder… | pass |  | 3179 |
| tests/eval/editor/corpus/configs/package.json | add `lodash` pinned to `^4.17.21` to dependencies. Also add `redis` pinned to `^… | pass |  | 2098 |
| tests/eval/editor/corpus/configs/package.json | add a `test` script that runs exactly the command `vitest run`, and add `vitest`… | pass |  | 3865 |
| tests/eval/editor/corpus/configs/tailwind.config.js | add a custom brand color `primary: '#1e40af'` under theme.extend.colors (keep th… | pass |  | 1135 |
| tests/eval/editor/corpus/configs/tailwind.config.js | extend the `content` array so it also scans `.vue` files under `./src/**/*`. Kee… | pass |  | 8225 |
| tests/eval/editor/corpus/configs/tsconfig.json | enable strict mode by setting `compilerOptions.strict` to the literal boolean tr… | pass |  | 2184 |
| tests/eval/editor/corpus/configs/tsconfig.json | add a `compilerOptions.paths` entry that maps `"@/*"` to the array `["src/*"]`. … | pass |  | 1302 |
| tests/eval/editor/corpus/cpp/CMakeLists.txt | append a line `target_compile_options(sampleapp PRIVATE -Wall -Wextra -Wpedantic… | pass |  | 1041 |
| tests/eval/editor/corpus/cpp/CMakeLists.txt | add `find_package(Threads REQUIRED)` near the top (after project()), and append … | edit_fail | edit /tmp/editor-eval-145417492/CMakeLists.txt: old_content not found in file (first 60 chars: "target_include_directories(sampleapp PRIVATE include)\n\n") | 1379 |
| tests/eval/editor/corpus/cpp/algorithm.cpp | at the top of top_k, throw `std::invalid_argument("k must be non-negative")` whe… | pass |  | 1979 |
| tests/eval/editor/corpus/cpp/algorithm.cpp | add a free function `std::string format_top_k(const std::vector<int>& input, int… | pass |  | 2662 |
| tests/eval/editor/corpus/cpp/compute.cpp | add a second parameter `bool doubleIt = false` to compute(int x). When doubleIt … | pass |  | 1093 |
| tests/eval/editor/corpus/cpp/math_class.hpp | add a public `void reset()` method that sets count_=0, sum_=T{}, sumSq_=T{}. Kee… | pass |  | 1985 |
| tests/eval/editor/corpus/cpp/math_class.hpp | add a public `T stddev() const` method that returns std::sqrt(variance()). Leave… | pass |  | 4034 |
| tests/eval/editor/corpus/golang/broken_import.go | this file does not compile because it's missing imports — add the minimum impo… | pass |  | 983 |
| tests/eval/editor/corpus/golang/go.mod | bump the `go` directive from 1.21 to 1.22. Leave require block intact. | pass |  | 729 |
| tests/eval/editor/corpus/golang/go.mod | add `github.com/gorilla/mux v1.8.1` as a required dependency in the require bloc… | pass |  | 1066 |
| tests/eval/editor/corpus/golang/http_server.go | add a GET /health endpoint that responds 200 with JSON body `{"status":"ok"}`. D… | pass |  | 5579 |
| tests/eval/editor/corpus/golang/http_server.go | replace the final `log.Fatal(http.ListenAndServe(":8080", nil))` with a graceful… | pass |  | 5790 |
| tests/eval/editor/corpus/golang/http_server.go | extract the anonymous function passed to http.HandleFunc("/users", ...) in main(… | pass |  | 3448 |
| tests/eval/editor/corpus/golang/struct_impl.go | add `func (i *Invoice) String() string` that returns the same text as Summary().… | pass |  | 1823 |
| tests/eval/editor/corpus/golang/struct_impl.go | change New() to return `(*Invoice, error)`. Return a non-nil error with a messag… | pass |  | 2942 |
| tests/eval/editor/corpus/golang/table_test.go | add a case `{"", ""}` to the cases slice. Don't remove or reorder existing cases… | pass |  | 1607 |
| tests/eval/editor/corpus/golang/table_test.go | wrap each iteration in `t.Run(c.in, func(t *testing.T) { ... })` so failures are… | pass |  | 1588 |
| tests/eval/editor/corpus/javascript/express_route.js | add a `router.delete('/users/:id', ...)` handler that removes the user from `use… | pass |  | 2829 |
| tests/eval/editor/corpus/javascript/express_route.js | convert the named `export { router }` at the bottom to a default export. No othe… | pass |  | 834 |
| tests/eval/editor/corpus/javascript/math.js | add a named export `cube(x)` that returns x*x*x. keep the existing `square` expo… | check_fail | exit status 1 — file:///tmp/tmp.KI455X6QTd/math.mjs:3 export function cube(x) { ^^^^^^  SyntaxError: Unexpected token 'export'     at compileSourceTextModule (node:internal/modules/esm/utils:346:16)… | 1196 |
| tests/eval/editor/corpus/javascript/math.js | in addition to the existing `square`, add named exports `cube(x)` returning x*x*… | pass |  | 1212 |
| tests/eval/editor/corpus/javascript/pinia_store.js | on the SUCCESS path of login (after this.token is set), persist the token to loc… | pass |  | 1947 |
| tests/eval/editor/corpus/javascript/pinia_store.js | add a new action `refreshUser()` that issues `axios.get('/api/users/me', { heade… | pass |  | 1426 |
| tests/eval/editor/corpus/javascript/react_button.jsx | add a `loading` boolean prop. When loading is true, the button must be disabled … | pass |  | 1790 |
| tests/eval/editor/corpus/javascript/react_button.jsx | wrap the exported Button in React.memo — the module must still expose a `Butto… | pass |  | 2023 |
| tests/eval/editor/corpus/javascript/vite.config.js | change the dev server port from 5173 to 5174; leave everything else untouched. | pass |  | 2840 |
| tests/eval/editor/corpus/javascript/vite.config.js | add a `resolve.alias` mapping the string '@' to the absolute path of the `src/` … | pass |  | 2566 |
| tests/eval/editor/corpus/other/ci.yml | add a second job named `lint` under jobs (parallel to `test`). It must run on ub… | pass |  | 1891 |
| tests/eval/editor/corpus/other/ci.yml | add an `actions/cache@v4` step to the `test` job, placed after setup-node and be… | pass |  | 1584 |
| tests/eval/editor/corpus/other/docker-compose.yml | add a `redis` service using image `redis:7-alpine`, publishing port 6379 to host… | pass |  | 1427 |
| tests/eval/editor/corpus/other/docker-compose.yml | add a healthcheck to the `db` service: test `["CMD-SHELL", "pg_isready -U app"]`… | pass |  | 1365 |
| tests/eval/editor/corpus/other/schema.sql | add a `created_at TEXT NOT NULL DEFAULT (datetime('now'))` column to the `users`… | pass |  | 1018 |
| tests/eval/editor/corpus/other/schema.sql | add a non-unique INDEX named `idx_events_user_time` on `events(user_id, occurred… | pass |  | 2079 |
| tests/eval/editor/corpus/python/cli_tool.py | add a `--verbose` / `-v` store_true flag. When set, print the literal string 'lo… | pass |  | 1695 |
| tests/eval/editor/corpus/python/cli_tool.py | wrap the body of main() so that a KeyboardInterrupt returns exit code 130 and pr… | check_fail | exit status 130 — Traceback (most recent call last):   File "<stdin>", line 8, in <module>   File "/tmp/tmp.qPPTnpRng9/cli_tool.py", line 25, in main     events = json.loads(args.input.read_text()) … | 1383 |
| tests/eval/editor/corpus/python/flask_app.py | add a GET /healthcheck route named `healthcheck` that returns JSON `{"status": "… | pass |  | 3946 |
| tests/eval/editor/corpus/python/flask_app.py | add a `before_request` hook that prints the request method and path separated by… | pass |  | 1589 |
| tests/eval/editor/corpus/python/flask_app.py | add a DELETE /users/<user_id> route that removes the user from USERS and returns… | pass |  | 1521 |
| tests/eval/editor/corpus/python/pandas_pipeline.py | in daily_totals, drop rows where revenue is null BEFORE computing the date colum… | pass |  | 2461 |
| tests/eval/editor/corpus/python/pandas_pipeline.py | change save_report to write Apache Parquet (via df.to_parquet) instead of CSV. K… | pass |  | 1239 |
| tests/eval/editor/corpus/python/price.py | change price(base, target) so it returns `base` if target is AT LEAST 7 days fro… | check_fail | exit status 1 — FAIL: price(100, now+7d) = 200, want 100  | 1688 |
| tests/eval/editor/corpus/python/pydantic_model.py | add a `created_at: datetime` field to User whose default is the current UTC time… | pass |  | 2615 |
| tests/eval/editor/corpus/python/pydantic_model.py | add a pydantic v2 `@field_validator('email')` classmethod on UserCreate that rai… | pass |  | 2169 |
| tests/eval/editor/corpus/typescript/ts_service.ts | rewrite both `get` and `list` to use async/await. They must still return the sam… | pass |  | 2110 |
| tests/eval/editor/corpus/typescript/ts_service.ts | add a private field `private cache = new Map<number, User>()`. In `get`, check c… | pass |  | 1975 |
| tests/eval/editor/corpus/typescript/ts_service.ts | add a `retries: number = 3` parameter to `get`. If the fetch response is not ok,… | check_fail | exit status 1 — node:internal/modules/run_main:123     triggerUncaughtException(     ^  AssertionError [ERR_ASSERTION]: retry loop missing     at file:///home/sites/kaiju/kaiju/[eval1]:1:206     at … | 1680 |
| tests/eval/editor/corpus/typescript/types.ts | add an exported TypeScript `enum Role` with exactly three values named Admin, Ed… | pass |  | 1042 |
| tests/eval/editor/corpus/typescript/types.ts | make the `name` field on User optional (append a `?` to the property key). Leave… | pass |  | 660 |
