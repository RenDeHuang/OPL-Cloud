# OPL Console Local Demo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a localhost-only, fake-only demo server that lets a human log into and interact with the React Console without production credentials or services.

**Architecture:** Reuse the Browser QA fixture as the single in-memory demo API authority. Serve Vite in middleware mode behind a Node HTTP server that handles `/api/*` locally and delegates all other paths to the existing React app.

**Tech Stack:** Node HTTP, Vite middleware mode, React 19, `@openai/apps-sdk-ui`, Node test runner, Playwright.

---

### Task 1: Freeze The Demo Boundary

**Files:**
- Create: `tests/ui/console-demo-server.test.ts`
- Modify: `package.json`

- [x] Write an integration test that imports `startConsoleDemoServer`, starts it on an ephemeral localhost port, and verifies unauthenticated, customer, Admin, and logout behavior.
- [x] Assert the package exposes `npm run demo` and the server origin is exactly `http://127.0.0.1:<port>`.
- [x] Run `node --test tests/ui/console-demo-server.test.ts` and verify RED because the demo server does not exist.

### Task 2: Share The Existing Fixture

**Files:**
- Modify: `tools/console-browser-qa.ts`

- [x] Export the fixed demo credentials, fixture state factory, and API fixture handler.
- [x] Make login select customer or Admin identity and make `/api/auth/me` require an in-memory session.
- [x] Preserve existing Browser QA behavior and keep fault injection configurable through fixture state.
- [x] Run the focused Browser QA and demo tests.

### Task 3: Add The Local Demo Server

**Files:**
- Create: `tools/start-console-demo.ts`
- Modify: `package.json`
- Modify: `README.md`

- [x] Start Vite in middleware mode and an HTTP server bound to `127.0.0.1`.
- [x] Adapt Node requests to the shared fake-only API handler without external network calls.
- [x] Print the URL and both credentials at startup; implement signal-safe close behavior.
- [x] Document that the command is local, in-memory, and non-production.

### Task 4: Browser Acceptance And Handoff

**Files:**
- Modify: `tests/ui/console-demo-server.test.ts`

- [x] Use Playwright against the real demo server to log in as customer, navigate to Workspace and API Service, then log in as Admin and open Operations Overview.
- [x] Run typecheck, lint, build, focused tests, full tests, and `git diff --check`.
- [x] Start `npm run demo` on the preview port and verify HTTP `200` before handing off the URL and credentials.
