import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const mainSource = await readFile(new URL("../../apps/console-ui/src/main.tsx", import.meta.url), "utf8");
const consolePageSource = await readFile(new URL("../../apps/console-ui/src/pages/ConsolePage.tsx", import.meta.url), "utf8");
const consoleStateSource = await readFile(new URL("../../apps/console-ui/src/store/console-state.ts", import.meta.url), "utf8");

test("operator UI authority comes from the server session", () => {
  assert.match(mainSource, /route\.requiresAdmin && session\?\.isOperator !== true/);
  assert.match(consolePageSource, /const isAdmin = session\.isOperator === true/);
  assert.doesNotMatch(consolePageSource, /session\.user\.role === ["']admin["']/);
});

test("tenant and operator startup state use separate APIs", () => {
  assert.match(consoleStateSource, /if \(isAdmin\) \{[\s\S]*getManagementState\(\)[\s\S]*setState\(management\)[\s\S]*return/);
  assert.match(consoleStateSource, /setState\(await getConsoleState\(accountId\)\)/);
  assert.doesNotMatch(consoleStateSource, /Promise\.all\(\[[\s\S]*getConsoleState\(accountId\)[\s\S]*getManagementState\(\)/);
});

test("initial Console failures leave the loading state", () => {
  assert.match(consoleStateSource, /const \[loadError, setLoadError\]/);
  assert.match(consolePageSource, /consoleState\.loadError/);
  assert.match(consolePageSource, /onLogout\(\)/);
  assert.match(consolePageSource, /重试/);
});
