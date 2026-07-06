import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { createHmac } from "node:crypto";
import { tmpdir } from "node:os";
import { join } from "node:path";

const DEFAULT_ACCOUNT_ID = "pi-production-verifier";
const DEFAULT_WORKSPACE_NAME = "Production Verification Lab";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 12;
const DEFAULT_RETRY_DELAY_MS = 5000;
const DEFAULT_MOUNT_PATH = "/data";
const WORKSPACE_PERSISTENCE_ROOT = "/data";
const DEFAULT_AIONUI_ADMIN_USERNAME = "admin";

function defaultRunId() {
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\..+$/, "Z");
  const suffix = Math.random().toString(36).slice(2, 8);
  return `${stamp}-${suffix}`;
}

function normalizeOrigin(origin) {
  if (!origin) throw new Error("origin_required");
  return origin.replace(/\/$/, "");
}

function isPrivateIpv4(hostname) {
  const parts = String(hostname || "").split(".").map((part) => Number(part));
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return false;
  const [first, second] = parts;
  return (
    first === 10 ||
    first === 127 ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 168) ||
    (first === 169 && second === 254) ||
    first === 0
  );
}

function isNonPublicHostname(hostname) {
  const normalized = String(hostname || "").toLowerCase();
  return (
    normalized === "localhost" ||
    normalized.endsWith(".localhost") ||
    normalized === "::1" ||
    normalized === "0:0:0:0:0:0:0:1" ||
    normalized.startsWith("fc") ||
    normalized.startsWith("fd") ||
    normalized.startsWith("fe80") ||
    isPrivateIpv4(normalized)
  );
}

function assertPublicHttpsUrl(url, errorName) {
  let parsed = null;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error(errorName);
  }
  if (parsed.protocol !== "https:" || isNonPublicHostname(parsed.hostname)) {
    throw new Error(errorName);
  }
  return parsed;
}

function assertConsoleOrigin(url, { allowPrivateConsoleOrigin = false } = {}) {
  if (!allowPrivateConsoleOrigin) return assertPublicHttpsUrl(url, "public_origin_required");
  let parsed = null;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error("console_origin_required");
  }
  if (!["http:", "https:"].includes(parsed.protocol)) throw new Error("console_origin_required");
  return parsed;
}

function endpoint(origin, path) {
  return `${normalizeOrigin(origin)}${path}`;
}

async function readResponse(response) {
  const contentType = response.headers?.get?.("content-type") || "";
  if (contentType.includes("application/json")) return response.json();
  return response.text();
}

function authHeaderValues(auth = null) {
  const headers = {};
  if (auth?.cookie) headers.cookie = auth.cookie;
  if (auth?.csrf) headers["x-opl-csrf-token"] = auth.csrf;
  return headers;
}

function requestHeaders({ body = null, auth = null } = {}) {
  const headers = {
    ...(body ? { "content-type": "application/json" } : {}),
    ...authHeaderValues(auth)
  };
  return Object.keys(headers).length > 0 ? headers : undefined;
}

async function requestJsonWithResponse({ fetchImpl, origin, path, method = "GET", body = null, auth = null }) {
  const response = await fetchImpl(endpoint(origin, path), {
    method,
    headers: requestHeaders({ body, auth }),
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    const error = new Error(`request_failed:${method}:${path}:${response.status}:${message}`);
    if (payload && typeof payload === "object") {
      error.safeMessage = payload.safeMessage || "";
      error.providerRequestId = payload.providerRequestId || payload.provider?.requestId || "";
      if (typeof payload.retryable === "boolean") error.retryable = payload.retryable;
      if (Array.isArray(payload.missingEnv)) error.missingEnv = payload.missingEnv;
    }
    throw error;
  }
  return { payload, response };
}

async function requestJson(args) {
  const { payload } = await requestJsonWithResponse(args);
  return payload;
}

function cookieHeaderFromSetCookie(setCookie = "") {
  return String(setCookie)
    .split(/,(?=[^;,]+=)/)
    .map((cookie) => cookie.split(";")[0]?.trim())
    .filter(Boolean)
    .join("; ");
}

function setCookieHeader(headers) {
  if (!headers) return "";
  const values = typeof headers.getSetCookie === "function" ? headers.getSetCookie() : [];
  if (values.length > 0) return values.join(",");
  return headers.get?.("set-cookie") || "";
}

function mergeCookieHeaders(...cookies) {
  return cookies.map((cookie) => String(cookie || "").trim()).filter(Boolean).join("; ");
}

function browserCookiesFromHeader(cookieHeader = "", url = "") {
  const parsed = new URL(url);
  return String(cookieHeader)
    .split(";")
    .map((entry) => entry.trim())
    .filter(Boolean)
    .map((entry) => {
      const index = entry.indexOf("=");
      return index > 0 ? {
        name: entry.slice(0, index),
        value: entry.slice(index + 1),
        domain: parsed.hostname,
        path: "/",
        secure: parsed.protocol === "https:"
      } : null;
    })
    .filter(Boolean);
}

function deriveAionUiAdminPassword(seed, workspaceId, token) {
  const secret = String(seed || "").trim();
  if (!secret || !workspaceId || !token) return "";
  const digest = createHmac("sha256", secret)
    .update(`${workspaceId}:${token}`)
    .digest("base64url")
    .slice(0, 24);
  return `opl_${digest}Aa1!`;
}

async function requestOperatorSession({ fetchImpl, origin, operatorToken }) {
  if (!operatorToken) return null;
  const { payload, response } = await requestJsonWithResponse({
    fetchImpl,
    origin,
    path: "/api/auth/operator-login",
    method: "POST",
    body: { operatorToken }
  });
  return {
    cookie: cookieHeaderFromSetCookie(setCookieHeader(response.headers)),
    csrf: response.headers?.get?.("x-opl-csrf-token") || payload?.csrfToken || ""
  };
}

function sleep(ms) {
  if (ms <= 0) return Promise.resolve();
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function requestWorkspaceUrl({ fetchImpl, url, attempts, retryDelayMs }) {
  let lastError = null;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    let requestUrl = url;
    let cookie = "";
    let response = await fetchImpl(requestUrl, { method: "GET", redirect: "manual" });
    if (response.status >= 300 && response.status < 400 && response.headers?.get?.("location")) {
      cookie = cookieHeaderFromSetCookie(setCookieHeader(response.headers));
      requestUrl = new URL(response.headers.get("location"), requestUrl).toString();
      response = await fetchImpl(requestUrl, {
        method: "GET",
        headers: cookie ? { cookie } : undefined
      });
    }
    const body = await response.text();
    if (response.ok) return { body, attempts: attempt, url: requestUrl, cookie };
    lastError = new Error(`workspace_url_failed:${response.status}:${body}`);
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw lastError;
}

function workspaceApiEndpoint(baseUrl, path) {
  const parsed = new URL(baseUrl);
  const normalizedPath = String(path || "").replace(/^\//, "");
  parsed.pathname = `${parsed.pathname.replace(/\/$/, "")}/${normalizedPath}`;
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString();
}

function workspaceApiBaseCandidates(workspaceUrl) {
  const root = new URL(workspaceUrl);
  root.pathname = "/";
  root.search = "";
  root.hash = "";
  const prefixed = new URL(workspaceUrl);
  prefixed.search = "";
  prefixed.hash = "";
  prefixed.pathname = prefixed.pathname.endsWith("/") ? prefixed.pathname : `${prefixed.pathname}/`;
  return [...new Set([root.toString(), prefixed.toString()])];
}

async function requestWorkspaceJson({ fetchImpl, workspaceUrl, path, method = "GET", body = null, cookie = "" }) {
  const response = await fetchImpl(workspaceApiEndpoint(workspaceUrl, path), {
    method,
    headers: {
      ...(body ? { "content-type": "application/json" } : {}),
      ...(cookie ? { cookie } : {})
    },
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    throw new Error(`workspace_api_failed:${method}:${path}:${response.status}:${message}`);
  }
  return payload;
}

async function requestWorkspaceWebuiLogin({ fetchImpl, workspaceAuth, username = DEFAULT_AIONUI_ADMIN_USERNAME, password = "" }) {
  if (!password) return workspaceAuth;
  let lastError = null;
  for (const apiBaseUrl of workspaceApiBaseCandidates(workspaceAuth.url)) {
    let response;
    let payload;
    try {
      response = await fetchImpl(workspaceApiEndpoint(apiBaseUrl, "/login"), {
        method: "POST",
        headers: {
          "content-type": "application/json",
          ...(workspaceAuth.cookie ? { cookie: workspaceAuth.cookie } : {})
        },
        body: JSON.stringify({ username, password, remember: false })
      });
      payload = await readResponse(response);
    } catch (error) {
      lastError = error;
      continue;
    }
    if (!response.ok) {
      const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
      lastError = new Error(`workspace_webui_login_failed:${response.status}:${message}`);
      continue;
    }
    const webuiCookie = cookieHeaderFromSetCookie(setCookieHeader(response.headers)) ||
      (typeof payload?.token === "string" ? `aionui-session=${payload.token}` : "");
    if (!webuiCookie) {
      lastError = new Error("workspace_webui_login_cookie_missing");
      continue;
    }
    return {
      ...workspaceAuth,
      apiBaseUrl,
      cookie: mergeCookieHeaders(workspaceAuth.cookie, webuiCookie)
    };
  }
  throw lastError || new Error("workspace_webui_login_failed");
}

function runtimePayloadData(payload) {
  if (payload && typeof payload === "object" && Object.hasOwn(payload, "data")) return payload.data;
  return payload;
}

async function verifyWorkspaceRuntimeFile({ fetchImpl, checks, workspaceUrl, runId, workspaceAuth = null }) {
  const filePath = `${WORKSPACE_PERSISTENCE_ROOT}/opl-e2e-${runId}.txt`;
  const content = `opl persistence ${runId}`;
  const authedWorkspaceUrl = workspaceAuth?.apiBaseUrl || workspaceAuth?.url || workspaceUrl;
  const cookie = workspaceAuth?.cookie || "";
  const user = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl: authedWorkspaceUrl,
    path: "/api/auth/user",
    cookie
  });
  addCheck(checks, "workspace_runtime_auth", Boolean(
    user?.success === true ||
    user?.user?.id ||
    user?.data?.user?.id
  ));

  const written = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl: authedWorkspaceUrl,
    path: "/api/fs/write",
    method: "POST",
    body: { path: filePath, data: content },
    cookie
  });
  addCheck(checks, "workspace_file_written", Boolean(
    written?.success === true ||
    runtimePayloadData(written) === true
  ), { path: filePath });

  const read = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl: authedWorkspaceUrl,
    path: "/api/fs/read",
    method: "POST",
    body: { path: filePath, workspace: WORKSPACE_PERSISTENCE_ROOT },
    cookie
  });
  addCheck(checks, "workspace_file_read", runtimePayloadData(read) === content, { path: filePath });
  return { filePath, content };
}

async function verifyWorkspacePersistedFile({ fetchImpl, checks, workspaceUrl, fileProof, workspaceAuth = null }) {
  const authedWorkspaceUrl = workspaceAuth?.apiBaseUrl || workspaceAuth?.url || workspaceUrl;
  const read = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl: authedWorkspaceUrl,
    path: "/api/fs/read",
    method: "POST",
    body: { path: fileProof.filePath, workspace: WORKSPACE_PERSISTENCE_ROOT },
    cookie: workspaceAuth?.cookie || ""
  });
  addCheck(checks, "workspace_persisted_file_read", runtimePayloadData(read) === fileProof.content, { path: fileProof.filePath });
}

async function defaultBrowserFactory() {
  try {
    return await import("playwright");
  } catch {
    throw new Error("playwright_required_for_browser_e2e");
  }
}

async function writeBrowserUploadFixture({ runId }) {
  const dir = await mkdtemp(join(tmpdir(), "opl-browser-e2e-"));
  const fileName = `opl-browser-e2e-${runId}.txt`;
  const filePath = join(dir, fileName);
  const content = `OPL_BROWSER_FILE_${runId}`;
  await writeFile(filePath, content, "utf8");
  return { fileName, filePath, content };
}

async function captureBrowserScreenshot({ page, screenshotDir, runId, suffix }) {
  if (!page || !screenshotDir) return "";
  const screenshotPath = join(screenshotDir, `workspace-browser-e2e-${runId}-${suffix}.png`);
  try {
    await mkdir(screenshotDir, { recursive: true });
    await page.screenshot({ path: screenshotPath, fullPage: true });
    return screenshotPath;
  } catch {
    return "";
  }
}

async function runBrowserCheck({ page, checks, name, screenshotDir, runId, successDetails = {}, recordSuccess = true, task }) {
  try {
    const result = await task();
    if (recordSuccess) addCheck(checks, name, true, successDetails);
    return result;
  } catch (cause) {
    const screenshotPath = await captureBrowserScreenshot({ page, screenshotDir, runId, suffix: "failure" });
    const details = {
      stage: name,
      ...(screenshotPath ? { screenshotPath } : {})
    };
    checks.push({
      name,
      ok: false,
      ...details,
      error: cause?.message || String(cause)
    });
    const error = new Error(`${name}_failed:${cause?.message || String(cause)}`);
    error.cause = cause;
    error.details = details;
    throw error;
  }
}

async function requireFirstFileInput(page) {
  const input = page.locator('input[type="file"]').first();
  if (await input.count() < 1) throw new Error("workspace_browser_file_input_missing");
  return input;
}

async function loginAionUiIfNeeded(page, { username = DEFAULT_AIONUI_ADMIN_USERNAME, password = "" } = {}) {
  if (!password || typeof page.locator !== "function") return false;
  const usernameInput = page.locator('input[name="username"], input#username, input[autocomplete="username"]').first();
  if (await usernameInput.count() < 1) return false;
  const passwordInput = page.locator('input[name="password"], input#password, input[type="password"], input[autocomplete="current-password"]').first();
  if (await passwordInput.count() < 1) throw new Error("workspace_browser_webui_password_input_missing");
  await usernameInput.fill(username);
  await passwordInput.fill(password);
  await page.getByRole("button", { name: /Sign In|登录|登入|Log in/i }).first().click({ timeout: 15_000 });
  await page.waitForFunction(() => {
    return !document.querySelector('input[name="password"], input#password, input[type="password"], input[autocomplete="current-password"]');
  }, {}, { timeout: 30_000 });
  return true;
}

async function selectDefaultWorkspaceAssistant(page) {
  try {
    await selectGuidWorkspaceAssistant(page);
    return;
  } catch {
    // Fall through to generic selectors for older/non-guid workspace shells.
  }
  let lastError = null;
  try {
    await page.getByRole("button", { name: /@Research|Research/i }).first().click({ timeout: 15_000 });
    await waitForWorkspaceAssistantSelection(page);
    return;
  } catch (error) {
    lastError = error;
    if (typeof page.getByText === "function") {
      try {
        await page.getByText(/@Research|Research/i).first().click({ timeout: 15_000 });
        await waitForWorkspaceAssistantSelection(page);
        return;
      } catch (textError) {
        lastError = textError;
      }
    }
  }
  try {
    await selectAnyVisibleWorkspaceAssistant(page);
    await waitForWorkspaceAssistantSelection(page);
    return;
  } catch (error) {
    lastError = error;
  }
  throw lastError || new Error("workspace_assistant_selection_failed");
}

async function selectAnyVisibleWorkspaceAssistant(page) {
  if (typeof page.evaluate !== "function") throw new Error("workspace_assistant_dom_unavailable");
  const selected = await page.evaluate(() => {
    const visible = (element) => {
      const rect = element.getBoundingClientRect();
      const style = window.getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    };
    const blocked = /Select an assistant|File\(|New Chat|Search|Scheduled Tasks|Runtime|Settings|Logout/i;
    const target = Array.from(document.querySelectorAll("button, [role='button'], div, li"))
      .filter(visible)
      .find((element) => {
        const text = (element.textContent || "").trim();
        return /^@[A-Za-z0-9][\s\S]{1,80}/.test(text) && !blocked.test(text);
      });
    if (!target) return false;
    target.click();
    return true;
  });
  if (!selected) throw new Error("workspace_assistant_card_not_found");
}

async function selectGuidWorkspaceAssistant(page) {
  if (typeof page.evaluate !== "function" || typeof page.waitForFunction !== "function") {
    throw new Error("workspace_guid_dom_unavailable");
  }
  await page.evaluate(() => {
    if (!window.location.hash.startsWith("#/guid")) window.location.hash = "#/guid";
  });
  await page.waitForFunction(() => {
    const visible = (element) => {
      if (!element) return false;
      const rect = element.getBoundingClientRect();
      const style = window.getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    };
    return Boolean(
      visible(document.querySelector('[data-testid="preset-pill-mas"]')) &&
      visible(document.querySelector('[data-testid="guid-input"] textarea, [data-testid="guid-input"]')) &&
      visible(document.querySelector('[data-testid="guid-send-btn"]'))
    );
  }, {}, { timeout: 15_000 });
  const selected = await page.evaluate(() => {
    const card = document.querySelector('[data-testid="preset-pill-mas"]');
    if (!card) return false;
    card.click();
    return true;
  });
  if (!selected) throw new Error("workspace_guid_assistant_missing");
  await waitForWorkspaceAssistantSelection(page);
}

async function waitForWorkspaceAssistantSelection(page) {
  await page.waitForFunction(() => {
    const text = document.body?.innerText || "";
    return !/Select an assistant to start a task/i.test(text);
  }, {}, { timeout: 15_000 });
}

async function clickSendControl(page) {
  try {
    await clickGuidSendControl(page);
    return;
  } catch {
    // Fall through to generic controls for older/non-guid workspace shells.
  }
  let lastError = null;
  try {
    await page.getByRole("button", { name: /发送|Send|提交|运行|Ask/i }).first().click({ timeout: 15_000 });
    return;
  } catch (error) {
    lastError = error;
  }
  const sendSelector = [
    'button[aria-label*="Send" i]',
    'button[title*="Send" i]',
    'button[aria-label*="发送" i]',
    'button[title*="发送" i]',
    'button[type="submit"]'
  ].join(", ");
  if (typeof page.locator === "function") {
    try {
      await page.locator(sendSelector).first().click({ timeout: 5_000 });
      return;
    } catch (error) {
      lastError = error;
    }
  }
  try {
    await clickRightmostComposerButton(page);
    return;
  } catch (error) {
    lastError = error;
  }
  throw new Error(`workspace_send_control_not_found:${lastError?.message || "unknown"}`);
}

async function clickRightmostComposerButton(page) {
  if (typeof page.evaluate !== "function") throw new Error("workspace_send_control_dom_unavailable");
  const clicked = await page.evaluate(() => {
    const visible = (element) => {
      const rect = element.getBoundingClientRect();
      const style = window.getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    };
    const buttons = Array.from(document.querySelectorAll("button"))
      .filter((button) => !button.disabled && visible(button));
    const explicit = buttons.find((button) => {
      const label = `${button.getAttribute("aria-label") || ""} ${button.getAttribute("title") || ""} ${button.innerText || ""}`;
      return /发送|Send|提交|运行|Ask/i.test(label);
    });
    if (explicit) {
      explicit.click();
      return true;
    }
    const inputs = Array.from(document.querySelectorAll("textarea, input[type='text'], [contenteditable='true']"))
      .filter(visible);
    const composer = inputs[inputs.length - 1];
    const composerRect = composer?.getBoundingClientRect();
    const candidates = buttons
      .filter((button) => !/@Research|@Grants|@PPT|Research|Grants|PPT|File/i.test(button.innerText || ""))
      .filter((button) => {
        if (!composerRect) return true;
        const rect = button.getBoundingClientRect();
        return rect.top >= composerRect.top - 96 && rect.bottom <= composerRect.bottom + 96;
      })
      .sort((left, right) => right.getBoundingClientRect().left - left.getBoundingClientRect().left);
    const button = candidates[0];
    if (!button) return false;
    button.click();
    return true;
  });
  if (!clicked) throw new Error("workspace_send_control_not_found");
}

async function clickGuidSendControl(page) {
  if (typeof page.evaluate !== "function") throw new Error("workspace_guid_send_dom_unavailable");
  const clicked = await page.evaluate(() => {
    const sendButton = document.querySelector('[data-testid="guid-send-btn"]');
    if (!sendButton) return false;
    if (sendButton.disabled || sendButton.getAttribute("disabled") !== null || sendButton.getAttribute("aria-disabled") === "true") {
      return false;
    }
    sendButton.click();
    return true;
  });
  if (!clicked) throw new Error("workspace_guid_send_disabled");
}

async function fillWorkspacePrompt(page, prompt) {
  try {
    await fillGuidWorkspacePrompt(page, prompt);
    return;
  } catch {
    // Fall through to generic composer selectors for older/non-guid workspace shells.
  }
  let lastError = null;
  const roleTextbox = page.getByRole("textbox");
  const attempts = [];
  if (typeof roleTextbox.last === "function") attempts.push(() => roleTextbox.last().fill(prompt));
  if (typeof page.locator === "function") {
    const composerSelector = "textarea, [contenteditable='true'], input[type='text']";
    const composer = page.locator(composerSelector);
    if (typeof composer.last === "function") attempts.push(() => composer.last().fill(prompt));
  }
  attempts.push(() => roleTextbox.first().fill(prompt));

  for (const attempt of attempts) {
    try {
      await attempt();
      await waitForComposerPrompt(page, prompt);
      return;
    } catch (error) {
      lastError = error;
    }
  }
  throw new Error(`workspace_prompt_fill_failed:${lastError?.message || "unknown"}`);
}

async function fillGuidWorkspacePrompt(page, prompt) {
  if (typeof page.evaluate !== "function") throw new Error("workspace_guid_input_dom_unavailable");
  const filled = await page.evaluate(({ prompt: expected }) => {
    const input = document.querySelector('[data-testid="guid-input"] textarea, [data-testid="guid-input"]');
    const sendButton = document.querySelector('[data-testid="guid-send-btn"]');
    if (!input || !sendButton) return false;
    const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")?.set ||
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
    if (!nativeSetter) return false;
    nativeSetter.call(input, expected);
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return (input.value || "").includes(expected);
  }, { prompt });
  if (!filled) throw new Error("workspace_guid_prompt_fill_failed");
  await waitForComposerPrompt(page, prompt);
}

async function waitForComposerPrompt(page, prompt) {
  await page.waitForFunction(({ prompt: expected }) => {
    const visible = (element) => {
      const rect = element.getBoundingClientRect();
      const style = window.getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    };
    return Array.from(document.querySelectorAll("textarea, [contenteditable='true'], input[type='text'], [role='textbox']"))
      .filter(visible)
      .some((element) => {
        const value = element.value || element.textContent || element.innerText || "";
        return value.includes(expected);
      });
  }, { prompt }, { timeout: 15_000 });
}

async function waitForSubmittedPrompt(page, prompt) {
  await page.waitForFunction(({ prompt: expected }) => {
    return (document.body?.innerText || "").includes(expected);
  }, { prompt }, { timeout: 15_000 });
}

export async function verifyWorkspaceBrowserUi({
  workspaceUrl,
  workspaceAuth = null,
  runId,
  checks,
  browserFactory = null,
  screenshotDir = "",
  launchOptions = { headless: true }
} = {}) {
  if (!workspaceUrl) throw new Error("workspace_url_required");
  if (!runId) throw new Error("run_id_required");
  const factory = browserFactory || await defaultBrowserFactory();
  if (!factory?.chromium?.launch) throw new Error("playwright_chromium_required");
  const browser = await factory.chromium.launch(launchOptions);
  try {
    let page;
    if (workspaceAuth?.cookie && browser.newContext) {
      const context = await browser.newContext();
      await context.addCookies(browserCookiesFromHeader(workspaceAuth.cookie, workspaceAuth.url || workspaceUrl));
      page = await context.newPage();
    } else {
      page = await browser.newPage();
    }
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_opened",
      screenshotDir,
      runId,
      successDetails: { url: workspaceUrl },
      task: () => page.goto(workspaceAuth?.url || workspaceUrl, { waitUntil: "networkidle", timeout: 120_000 })
    });
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_webui_login",
      screenshotDir,
      runId,
      recordSuccess: Boolean(workspaceAuth?.webuiPassword),
      task: () => loginAionUiIfNeeded(page, {
        username: workspaceAuth?.webuiUsername,
        password: workspaceAuth?.webuiPassword
      })
    });

    const fixture = await writeBrowserUploadFixture({ runId });
    const fileInput = await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_input",
      screenshotDir,
      runId,
      recordSuccess: false,
      task: () => requireFirstFileInput(page)
    });
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_uploaded",
      screenshotDir,
      runId,
      successDetails: { fileName: fixture.fileName },
      task: () => fileInput.setInputFiles(fixture.filePath)
    });

    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_read",
      screenshotDir,
      runId,
      successDetails: { fileName: fixture.fileName },
      task: () => page.waitForFunction(({ fileName, content }) => {
        const text = document.body?.innerText || "";
        return text.includes(fileName) || text.includes(content);
      }, { fileName: fixture.fileName, content: fixture.content }, { timeout: 60_000 })
    });

    const marker = `OPL_BROWSER_E2E_${runId}`;
    const prompt = `请只回复：${marker}`;
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_message_sent",
      screenshotDir,
      runId,
      task: async () => {
        await selectDefaultWorkspaceAssistant(page);
        await fillWorkspacePrompt(page, prompt);
        await clickSendControl(page);
        await waitForSubmittedPrompt(page, prompt);
      }
    });

    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_reply_seen",
      screenshotDir,
      runId,
      successDetails: { marker },
      task: () => page.waitForFunction(({ marker: expected }) => {
        const text = document.body?.innerText || "";
        const prompt = `请只回复：${expected}`;
        let count = 0;
        let index = 0;
        while ((index = text.indexOf(expected, index)) !== -1) {
          count += 1;
          index += expected.length;
        }
        return count >= 2 || (text.includes(expected) && !text.includes(prompt));
      }, { marker }, { timeout: 180_000 })
    });

    await captureBrowserScreenshot({ page, screenshotDir, runId, suffix: "success" });
  } finally {
    await browser.close();
  }
}

async function requestRuntimeStatus({ fetchImpl, origin, accountId, workspaceId, attempts, retryDelayMs, auth = null }) {
  let lastStatus = null;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const status = await requestJson({
      fetchImpl,
      origin,
      path: "/api/workspaces/runtime-status",
      method: "POST",
      auth,
      body: { accountId, workspaceId }
    });
    lastStatus = { ...status, attempts: attempt };
    if (
      status?.ready === true &&
      Array.isArray(status.checks) &&
      status.checks.length > 0 &&
      status.checks.every((check) => check.ok === true)
    ) {
      return lastStatus;
    }
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  return lastStatus;
}

function addCheck(checks, name, ok, details = {}) {
  const check = { name, ok: Boolean(ok), ...details };
  checks.push(check);
  if (!check.ok) {
    const error = new Error(`${name}_failed`);
    error.details = details;
    throw error;
  }
  return check;
}

function assertReady({ checks, name, payload }) {
  if (!payload.ready) {
    const failed = payload.failedChecks?.length ? payload.failedChecks.join(",") : "unknown";
    throw new Error(`${name}_not_ready:${failed}`);
  }
  addCheck(checks, name, true);
}

function assertComputeShape(checks, compute, name = "compute_created") {
  addCheck(checks, name, Boolean(
    compute?.id &&
    compute?.provider === "tencent-tke" &&
    compute?.nodeName &&
    (compute?.instanceId || compute?.providerResourceId) &&
    compute?.status === "running" &&
    compute?.billingStatus === "active"
  ), { computeAllocationId: compute?.id });
}

async function waitForComputeReady({
  fetchImpl,
  origin,
  accountId,
  compute,
  attempts,
  retryDelayMs,
  auth = null,
  checks,
  name = "compute_created"
}) {
  let current = compute;
  for (let attempt = 0; attempt <= attempts; attempt += 1) {
    if (
      current?.id &&
      current?.provider === "tencent-tke" &&
      current?.nodeName &&
      (current?.instanceId || current?.providerResourceId) &&
      current?.status === "running" &&
      current?.billingStatus === "active"
    ) {
      addCheck(checks, name, true, {
        computeAllocationId: current.id,
        nodeName: current.nodeName,
        attempts: attempt + 1
      });
      return current;
    }
    if (!current?.id) break;
    if (attempt >= attempts) break;
    if (attempt > 0) await sleep(retryDelayMs);
    current = await requestJson({
      fetchImpl,
      origin,
      path: `/api/compute-allocations/${encodeURIComponent(current.id)}?accountId=${encodeURIComponent(accountId)}`,
      auth
    });
  }
  assertComputeShape(checks, current, name);
  return current;
}

function assertStorageShape(checks, storage) {
  addCheck(checks, "storage_created", Boolean(
    storage?.id &&
    storage?.provider === "tencent-tke" &&
    storage?.providerResourceId?.startsWith("pvc/") &&
    storage?.status === "available" &&
    storage?.billingStatus === "active" &&
    Number(storage?.sizeGb || 0) > 0
  ), { storageId: storage?.id });
}

function assertAttachmentShape(checks, attachment, { compute, storage }, name = "storage_attached") {
  addCheck(checks, name, Boolean(
    attachment?.id &&
    attachment?.provider === "tencent-tke" &&
    attachment?.computeAllocationId === compute?.id &&
    attachment?.storageId === storage?.id &&
    attachment?.mountPath === DEFAULT_MOUNT_PATH &&
    attachment?.status === "attached"
  ), { attachmentId: attachment?.id });
}

function assertWorkspaceShape(checks, workspace, { compute, storage, attachment }, name = "workspace_created") {
  addCheck(checks, name, Boolean(
    workspace?.id &&
    workspace?.provider === "tencent-tke" &&
    workspace?.computeAllocationId === compute?.id &&
    workspace?.storageId === storage?.id &&
    workspace?.attachmentId === attachment?.id &&
    workspace?.url &&
    workspace?.access?.tokenStatus === "active"
  ), { workspaceId: workspace?.id });
}

function assertRuntimeStatus(checks, runtimeStatus, name = "workspace_runtime_status") {
  const runtimeChecks = (runtimeStatus?.checks || []).map((check) => ({
    name: check?.name,
    ok: check?.ok === true
  }));
  const failedChecks = runtimeChecks.filter((check) => !check.ok).map((check) => check.name);
  addCheck(checks, name, Boolean(
    runtimeStatus?.ready === true &&
    Array.isArray(runtimeStatus.checks) &&
    runtimeStatus.checks.length > 0 &&
    runtimeStatus.checks.every((check) => check.ok === true)
  ), {
    runtimeChecks,
    failedChecks,
    attempts: runtimeStatus?.attempts
  });
}

function assertResourceBillingSettlement(checks, settlement, { accountId, compute, storage }) {
  const entries = settlement?.entries || [];
  const hasComputeDebit = entries.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageDebit = entries.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.type === "storage_debit"
  );

  addCheck(checks, "resource_billing_settled", Boolean(hasComputeDebit && hasStorageDebit), {
    entryTypes: entries.map((entry) => entry.type)
  });
}

function assertLedgerAndUsage(checks, state, { accountId, compute, storage, attachment }) {
  const ledger = state?.billingLedger || [];
  const resourceUsage = state?.resourceUsageLogs || [];
  const walletTransactions = state?.walletTransactions || [];

  const hasComputeLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const hasAttachmentLedger = ledger.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasComputeUsage = resourceUsage.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.resourceType === "compute"
  );
  const hasStorageUsage = resourceUsage.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.resourceType === "storage"
  );
  const hasAttachmentUsage = resourceUsage.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasComputeWalletTransaction = walletTransactions.some((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageWalletTransaction = walletTransactions.some((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const missingChecks = [
    [hasComputeLedger, "compute_ledger"],
    [hasStorageLedger, "storage_ledger"],
    [hasAttachmentLedger, "attachment_ledger"],
    [hasComputeUsage, "compute_usage"],
    [hasStorageUsage, "storage_usage"],
    [hasAttachmentUsage, "attachment_usage"],
    [hasComputeWalletTransaction, "compute_wallet_transaction"],
    [hasStorageWalletTransaction, "storage_wallet_transaction"]
  ].filter(([ok]) => !ok).map(([, name]) => name);

  addCheck(checks, "ledger_and_usage_verified", Boolean(
    state?.wallet?.accountId === accountId &&
    hasComputeLedger &&
    hasStorageLedger &&
    hasAttachmentLedger &&
    hasComputeUsage &&
    hasStorageUsage &&
    hasAttachmentUsage &&
    hasComputeWalletTransaction &&
    hasStorageWalletTransaction
  ), { missingChecks });
}

function compactObject(payload) {
  return Object.fromEntries(Object.entries(payload).filter(([, value]) => value));
}

function verificationResourceIds({ compute, storage, attachment, workspace, replacementCompute, replacementAttachment, replacementWorkspace }) {
  return compactObject({
    computeAllocationId: compute?.id,
    storageId: storage?.id,
    attachmentId: attachment?.id,
    workspaceId: workspace?.id,
    replacementComputeAllocationId: replacementCompute?.id,
    replacementAttachmentId: replacementAttachment?.id,
    replacementWorkspaceId: replacementWorkspace?.id
  });
}

async function cleanupVerificationResources({ fetchImpl, origin, accountId, computeAllocationId, storageId, attachmentId, checks = null, auth = null }) {
  const cleanupErrors = [];

  if (attachmentId) {
    try {
      const detached = await requestJson({
        fetchImpl,
        origin,
        path: "/api/storage-attachments/detach",
        method: "POST",
        auth,
        body: { accountId, attachmentId, confirm: true }
      });
      if (checks) {
        addCheck(checks, "verification_storage_detached", Boolean(detached?.status === "detached"));
      }
    } catch (error) {
      cleanupErrors.push(`detach_storage:${error.message}`);
    }
  }

  if (computeAllocationId) {
    try {
      const destroyed = await requestJson({
        fetchImpl,
        origin,
        path: `/api/compute-allocations/${encodeURIComponent(computeAllocationId)}/destroy`,
        method: "POST",
        auth,
        body: { accountId, computeAllocationId, confirm: true }
      });
      if (checks) {
        addCheck(checks, "verification_compute_destroyed", Boolean(
          destroyed?.status === "destroyed" &&
          destroyed?.billingStatus === "stopped"
        ));
      }
    } catch (error) {
      cleanupErrors.push(`destroy_compute:${error.message}`);
    }
  }

  if (storageId) {
    try {
      const destroyed = await requestJson({
        fetchImpl,
        origin,
        path: "/api/storage-volumes/destroy",
        method: "POST",
        auth,
        body: { accountId, storageId, confirmDataLoss: true }
      });
      if (checks) {
        addCheck(checks, "verification_storage_destroyed", Boolean(
          destroyed?.status === "destroyed" &&
          destroyed?.billingStatus === "stopped"
        ));
      }
    } catch (error) {
      cleanupErrors.push(`destroy_storage:${error.message}`);
    }
  }

  return cleanupErrors;
}

export async function verifyProductionChain({
  origin,
  accountId = DEFAULT_ACCOUNT_ID,
  workspaceName,
  runId = defaultRunId(),
  packageId = DEFAULT_PACKAGE_ID,
  creditAmount = DEFAULT_CREDIT_AMOUNT,
  workspaceUrlAttempts = DEFAULT_WORKSPACE_URL_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  operatorToken = "",
  allowPrivateConsoleOrigin = false,
  browserE2E = false,
  browserFactory = null,
  screenshotDir = "",
  fetchImpl = globalThis.fetch
} = {}) {
  if (typeof fetchImpl !== "function") throw new Error("fetch_required");
  const checks = [];
  const normalizedOrigin = normalizeOrigin(origin);
  assertConsoleOrigin(normalizedOrigin, { allowPrivateConsoleOrigin });
  const effectiveWorkspaceName = workspaceName || `${DEFAULT_WORKSPACE_NAME} ${runId}`;
  const creditSourceEventId = `production_verification_credit:${runId}`;
  const computeName = `${effectiveWorkspaceName} compute ${runId}`;
  const storageName = `${effectiveWorkspaceName} storage ${runId}`;
  let compute = null;
  let storage = null;
  let attachment = null;
  let workspace = null;
  let replacementCompute = null;
  let replacementAttachment = null;
  let replacementWorkspace = null;
  let auth = null;
  let firstComputeForLedger = null;
  let firstAttachmentForLedger = null;

  try {
    const productionReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
    assertReady({ checks, name: "production_readiness", payload: productionReadiness });

    const runtimeReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/runtime/readiness" });
    assertReady({ checks, name: "runtime_readiness", payload: runtimeReadiness });

    auth = await requestOperatorSession({ fetchImpl, origin: normalizedOrigin, operatorToken });

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/topups",
      method: "POST",
      auth,
      body: { accountId, amount: creditAmount, reason: creditSourceEventId }
    });

    compute = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-allocations",
      method: "POST",
      auth,
      body: { accountId, packageId, name: computeName }
    });
    compute = await waitForComputeReady({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      compute,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth,
      checks
    });

    storage = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-volumes",
      method: "POST",
      auth,
      body: { accountId, packageId, name: storageName }
    });
    assertStorageShape(checks, storage);

    attachment = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeAllocationId: compute.id,
        storageId: storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    assertAttachmentShape(checks, attachment, { compute, storage });

    workspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: effectiveWorkspaceName, attachmentId: attachment.id }
    });
    assertWorkspaceShape(checks, workspace, { compute, storage, attachment });

    const runtimeStatus = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspace.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, runtimeStatus);

    assertPublicHttpsUrl(workspace.url, "public_workspace_url_required");
    const workspaceUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: workspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "workspace_url", true, { url: workspace.url, attempts: workspaceUrlResult.attempts });
    const webuiUsername = DEFAULT_AIONUI_ADMIN_USERNAME;
    const webuiPassword = deriveAionUiAdminPassword(
      process.env.OPL_AIONUI_ADMIN_PASSWORD_SEED,
      workspace.id,
      workspace.access?.token || ""
    );
    const workspaceApiAuth = await requestWorkspaceWebuiLogin({
      fetchImpl,
      workspaceAuth: workspaceUrlResult,
      username: webuiUsername,
      password: webuiPassword
    });
    if (browserE2E) {
      await verifyWorkspaceBrowserUi({
        workspaceUrl: workspace.url,
        workspaceAuth: {
          ...workspaceApiAuth,
          webuiUsername,
          webuiPassword
        },
        runId,
        checks,
        browserFactory,
        screenshotDir
      });
    }
    const fileProof = await verifyWorkspaceRuntimeFile({
      fetchImpl,
      checks,
      workspaceUrl: workspace.url,
      runId,
      workspaceAuth: workspaceApiAuth
    });

    firstComputeForLedger = compute;
    firstAttachmentForLedger = attachment;
    const firstCleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: compute.id,
      attachmentId: attachment.id,
      checks,
      auth
    });
    if (firstCleanupErrors.length > 0) {
      const error = new Error(`production_verification_cleanup_failed:${firstCleanupErrors.join("|")}`);
      error.cleanupErrors = firstCleanupErrors;
      throw error;
    }
    compute = null;
    attachment = null;

    replacementCompute = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-allocations",
      method: "POST",
      auth,
      body: { accountId, packageId, name: `${effectiveWorkspaceName} replacement compute ${runId}` }
    });
    replacementCompute = await waitForComputeReady({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      compute: replacementCompute,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth,
      checks,
      name: "replacement_compute_created"
    });

    replacementAttachment = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeAllocationId: replacementCompute.id,
        storageId: storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    assertAttachmentShape(checks, replacementAttachment, { compute: replacementCompute, storage }, "replacement_storage_attached");

    replacementWorkspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: effectiveWorkspaceName, attachmentId: replacementAttachment.id }
    });
    assertWorkspaceShape(checks, replacementWorkspace, {
      compute: replacementCompute,
      storage,
      attachment: replacementAttachment
    }, "replacement_workspace_created");

    const replacementRuntimeStatus = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: replacementWorkspace.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, replacementRuntimeStatus, "replacement_workspace_runtime_status");

    assertPublicHttpsUrl(replacementWorkspace.url, "public_workspace_url_required");
    const replacementWorkspaceUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: replacementWorkspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "replacement_workspace_url", true, {
      url: replacementWorkspace.url,
      attempts: replacementWorkspaceUrlResult.attempts
    });
    const replacementWorkspaceApiAuth = await requestWorkspaceWebuiLogin({
      fetchImpl,
      workspaceAuth: replacementWorkspaceUrlResult,
      username: webuiUsername,
      password: webuiPassword
    });
    await verifyWorkspacePersistedFile({
      fetchImpl,
      checks,
      workspaceUrl: replacementWorkspace.url,
      fileProof,
      workspaceAuth: replacementWorkspaceApiAuth
    });

    const resourceSettlement = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/resource-settlements",
      method: "POST",
      auth,
      body: {
        accountId,
        hours: 1,
        sourceEventId: `production_verification_resource_settlement:${runId}`
      }
    });
    assertResourceBillingSettlement(checks, resourceSettlement, {
      accountId,
      compute: replacementCompute,
      storage
    });

    const state = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: `/api/state?accountId=${encodeURIComponent(accountId)}`,
      auth
    });
    assertLedgerAndUsage(checks, state, {
      accountId,
      compute: replacementCompute,
      storage,
      attachment: firstAttachmentForLedger || attachment
    });

    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: replacementCompute.id,
      storageId: storage.id,
      attachmentId: replacementAttachment.id,
      checks,
      auth
    });
    if (cleanupErrors.length > 0) {
      const error = new Error(`production_verification_cleanup_failed:${cleanupErrors.join("|")}`);
      error.cleanupErrors = cleanupErrors;
      throw error;
    }

    return {
      ok: true,
      accountId,
      runId,
      workspaceId: workspace.id,
      workspaceName: effectiveWorkspaceName,
      url: workspace.url,
      checks
    };
  } catch (error) {
    if (!compute?.id && !storage?.id && !attachment?.id && !replacementCompute?.id && !replacementAttachment?.id) throw error;
    error.accountId = accountId;
    error.runId = runId;
    error.workspaceName = effectiveWorkspaceName;
    error.resourceIds = verificationResourceIds({
      compute,
      storage,
      attachment,
      workspace,
      replacementCompute,
      replacementAttachment,
      replacementWorkspace
    });
    error.checks = checks;
    const cleanupErrors = [];
    if (replacementCompute?.id || replacementAttachment?.id) {
      const replacementCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        computeAllocationId: replacementCompute?.id,
        attachmentId: replacementAttachment?.id,
        auth
      });
      cleanupErrors.push(...replacementCleanupErrors);
    }
    if (compute?.id || attachment?.id) {
      const primaryCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        computeAllocationId: compute?.id,
        attachmentId: attachment?.id,
        auth
      });
      cleanupErrors.push(...primaryCleanupErrors);
    }
    if (storage?.id) {
      const storageCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        storageId: storage.id,
        auth
      });
      cleanupErrors.push(...storageCleanupErrors);
    }
    if (cleanupErrors.length > 0) error.cleanupErrors = [...(error.cleanupErrors || []), ...cleanupErrors];
    throw error;
  }
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const value = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
    args[key] = value;
  }
  return args;
}

function verifierOptionsFromArgs({ argv, env = process.env, fetchImpl = globalThis.fetch }) {
  const args = cliArgs(argv);
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || DEFAULT_ACCOUNT_ID,
    workspaceName: args.workspace || env.OPL_VERIFY_WORKSPACE_NAME,
    runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
    packageId: args.package || env.OPL_VERIFY_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    creditAmount: Number(args.credit || env.OPL_VERIFY_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_WORKSPACE_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    operatorToken: args["operator-token"] || env.OPL_VERIFY_OPERATOR_TOKEN || "",
    browserE2E: ["1", "true"].includes(String(args["browser-e2e"] || env.OPL_VERIFY_BROWSER_E2E || "").toLowerCase()),
    screenshotDir: args["screenshot-dir"] || env.OPL_VERIFY_SCREENSHOT_DIR || "",
    fetchImpl
  };
}

function errorPayload(error) {
  return {
    ok: false,
    error: error.message,
    ...(error.safeMessage ? { safeMessage: error.safeMessage } : {}),
    ...(error.providerRequestId ? { providerRequestId: error.providerRequestId } : {}),
    ...(typeof error.retryable === "boolean" ? { retryable: error.retryable } : {}),
    ...(Array.isArray(error.missingEnv) ? { missingEnv: error.missingEnv } : {}),
    ...(error.details ? { details: error.details } : {}),
    ...(error.resourceIds ? { resourceIds: error.resourceIds } : {}),
    ...(error.accountId ? { accountId: error.accountId } : {}),
    ...(error.runId ? { runId: error.runId } : {}),
    ...(error.workspaceName ? { workspaceName: error.workspaceName } : {}),
    ...(error.checks ? { checks: error.checks } : {}),
    ...(error.cleanupErrors ? { cleanupErrors: error.cleanupErrors } : {})
  };
}

export async function runProductionVerifierCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  try {
    const result = await verifyProductionChain(verifierOptionsFromArgs({ argv, env, fetchImpl }));
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify(errorPayload(error), null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionVerifierCli().then((code) => {
    process.exitCode = code;
  });
}
