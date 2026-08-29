---
name: playwright
slug: playwright
version: 1.0.3
homepage: https://clawic.com/skills/playwright
description: "Browser automation with Playwright. Drive a real browser to navigate sites, click elements, fill forms, take screenshots, extract data from JavaScript-rendered pages, and write or run end-to-end tests. Use when a static fetch is not enough because the page renders its content in the browser, or the task needs interaction, authentication, uploads, downloads, or the visible rendering of a page."
metadata:
  requires:
    bins: ["node", "npx"]
  os: ["linux", "darwin", "win32"]
---

## When to Use

Use this skill when success depends on a real browser: pages that build their content with JavaScript, multi-step forms, logged-in views, uploads and downloads, screenshots and PDFs, and end-to-end tests of a running application.

Do not use it when a plain HTTP request would answer the question. `web_fetch` and `web_search` are faster, cheaper and steadier than a browser, and a page that serves its content in the initial HTML needs nothing more. Reach for a browser once you have evidence that the plain fetch came back without the content.

## Planning Guidance

This system has no browser tool. Every browser action runs as Node code that drives Playwright, so a browser step is one of two shapes:

- `bash` running a script that already exists, or a one-line `node -e`, or an existing test suite (`npx playwright test`).
- `compute` when the browser work needs a script that has not been written yet. Ask it for a complete script and a command that runs it.

Before the first browser step, establish that Playwright is there. `npx playwright --version` answers it, and `npx playwright install chromium` installs the browser binary, which is a separate download from the npm package and is the usual reason a first run fails. Both are `bash` steps, and both belong in the plan rather than inside the script.

Run headless. This machine ordinarily has no display, so a headed run and `npx playwright codegen` both fail with a missing-display error rather than showing anything. To learn a page's structure, have the script write the rendered HTML or an accessibility snapshot to a file, then read that file.

A browser step's real output is a file: a screenshot, a PDF, a download, a trace, or extracted data. Write it into the workspace under a name you choose, and have the next step read that path. For a screenshot, `image_read` is the step that looks at it; for extracted data, write JSON and read it with `file_read`.

Serve the application before testing it. A suite pointed at a server nobody started fails on connection refused, and the fix is a `service` step for the server, not a retry of the test.

There are reference files beside this card, in the `playwright` directory under the skills directory: `selectors.md`, `debugging.md`, `testing.md`, `ci-cd.md` and `scraping.md`. They hold the detail this card summarises. Locate the directory with `file_list` and read the one file the task needs, not all five.

## RULES

1. **Test what the user sees.** Use a browser when the outcome depends on rendered interface, actionability, authentication, uploads, downloads or navigation. Anything a unit test or a direct API call can check should be checked that way instead.

2. **Isolate a run before making it clever.** Keep scripts and tests independent, so a retry or a parallel run inherits no state from the last one. Where a repository already has a Playwright configuration, fixtures and authentication setup, extend those rather than building a second arrangement alongside them.

3. **Look before you act.** Open the page and capture its rendered state before fixing on selectors or assertions. Guessing a selector from the page source is how automation works once and then stops working. When a failure only appears sometimes, capture a trace before rewriting anything.

4. **Prefer locators that survive a redesign.** Role, label, text, alt text, title and test id, ahead of CSS and XPath. Assert the visible outcome rather than that a click was issued. Where a locator matches several elements, narrow it; `first()`, `last()` and `nth()` silence the ambiguity without resolving it, and belong only where position is the thing under test.

5. **Wait on the application, not on the clock.** Playwright's own actionability checks cover most waiting. Where they do not, wait on an expectation, a URL, a response or a signal the application gives when it is ready. A fixed sleep passes on a fast machine and fails on a slow one. `networkidle` never settles on a page that polls.

6. **Control what you do not own.** Mock third-party services, upstream APIs and analytics when the point is to verify this application. For extracting data, check whether the site publishes an API or serves the content in plain HTML before driving a browser at it.

7. **Keep credentials and production access deliberate.** Do not persist browser state by default. Reuse a saved authentication state only where the repository already standardises it. For anything that spends money, changes production data, or cannot be undone, use a staging or local target.

## Playwright Traps

- Guessing selectors from page source, or using `first()` to silence an ambiguous match — works once, then fails on the next deploy.
- Building a new test structure next to the repository's existing configuration, fixtures and conventions — the two fight each other.
- Asserting on internals rather than visible outcomes — the suite passes while the user's path is broken.
- One authenticated session shared across parallel tests that write to the server — failures become order-dependent.
- `force: true` before understanding the overlay or the disabled state that blocked the click — hides the bug the test was meant to catch.
- Waiting for `networkidle` on a page with polling or a socket — the page is never idle.
- Driving a browser where an HTTP request would do — slower, less reliable, and no more informative.
- Running headed, or running `codegen`, on a machine with no display — fails before the browser opens.

## Debug Guidance

When a browser step fails, read the error before changing the script.

- **`Executable doesn't exist` or a missing browser path** — the browser binary was never installed. Run `npx playwright install chromium`. Installing the npm package does not install the browser.
- **`Missing X server or $DISPLAY`** — something asked for a headed browser. Set `headless: true`, or drop `--headed`.
- **`connect ECONNREFUSED`** — nothing is serving the target. Start the application as a service and confirm it answers before running the test again.
- **`Timeout ... waiting for locator`** — the element never reached the state the action needed. Screenshot the page at the moment of failure and look at it. The element may be absent, behind an overlay, inside an iframe, or still loading.
- **`strict mode violation`** — the locator matched more than one element. Narrow it; do not add `first()`.
- **A test that passes alone and fails alongside others** — shared state. Look at what the tests write, not at their timing.

Capture a trace (`--trace on`) before rewriting a test that fails only sometimes. The trace shows what the page looked like at each step, which guessing does not.

## Security and Privacy

Requests go to the sites the task names, carrying whatever the automation types into them, including any credentials it is given. Screenshots, traces, videos, PDFs and downloads stay in the workspace, and a screenshot of a logged-in page holds whatever was on screen. Installing Playwright fetches packages from the npm registry and browser binaries from Playwright's own download host.
