import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Vite dev proxy can point a UI worktree at an isolated Console API", async () => {
  const source = await readFile(new URL("../../vite.config.js", import.meta.url), "utf8");

  assert.match(source, /OPL_CONSOLE_API_ORIGIN/, "Vite proxy must be configurable for isolated UI worktrees");
  assert.match(source, /http:\/\/127\.0\.0\.1:8787/, "Vite proxy must keep the existing local API fallback");
});
