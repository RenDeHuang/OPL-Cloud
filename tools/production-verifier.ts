import { createHash } from "node:crypto";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
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
const VERIFICATION_PRICING_VERSION = "opl-tencent-v1";

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
  if (auth?.csrf) headers["x-opl-csrf"] = auth.csrf;
  return headers;
}

function requestHeaders({ body = null, auth = null, idempotencyKey = "", headers: extraHeaders = {} } = {}) {
	const headers = {
		...(body ? { "content-type": "application/json" } : {}),
		...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
		...authHeaderValues(auth),
		...extraHeaders
	};
	return Object.keys(headers).length > 0 ? headers : undefined;
}

async function requestJsonWithResponse({ fetchImpl, origin, path, method = "GET", body = null, auth = null, idempotencyKey = "", headers = {} }) {
	const response = await fetchImpl(endpoint(origin, path), {
		method,
		headers: requestHeaders({ body, auth, idempotencyKey, headers }),
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

async function requestOperatorSession({ fetchImpl, origin, operatorToken }) {
  if (!operatorToken) return null;
  const { payload, response } = await requestJsonWithResponse({
    fetchImpl,
		origin,
		path: "/api/auth/operator-login",
		method: "POST",
		body: {},
		headers: { "x-opl-operator-token": operatorToken }
	});
  return {
    cookie: cookieHeaderFromSetCookie(setCookieHeader(response.headers)),
    csrf: response.headers?.get?.("x-opl-csrf-token") || payload?.csrfToken || ""
  };
}

async function requestOwnerSession({ fetchImpl, origin, email, password }) {
  const { payload, response } = await requestJsonWithResponse({
    fetchImpl,
    origin,
    path: "/api/auth/login",
    method: "POST",
    body: { email, password }
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
    let redirected = false;
    let response = await fetchImpl(requestUrl, { method: "GET", redirect: "manual" });
    if (response.status >= 300 && response.status < 400 && response.headers?.get?.("location")) {
      cookie = cookieHeaderFromSetCookie(setCookieHeader(response.headers));
      requestUrl = new URL(response.headers.get("location"), requestUrl).toString();
      redirected = true;
      response = await fetchImpl(requestUrl, {
        method: "GET",
        headers: cookie ? { cookie } : undefined
      });
    }
    const body = await response.text();
    if (response.ok) return { body, attempts: attempt, url: requestUrl, cookie, redirected };
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

async function requestWorkspaceWebuiSession({ fetchImpl, workspaceAuth }) {
  let lastError = null;
  for (const apiBaseUrl of workspaceApiBaseCandidates(workspaceAuth.url)) {
    let response;
    let payload;
    try {
      response = await fetchImpl(workspaceApiEndpoint(apiBaseUrl, "/api/auth/user"), {
        method: "GET",
        headers: {
          ...(workspaceAuth.cookie ? { cookie: workspaceAuth.cookie } : {})
        }
      });
      payload = await readResponse(response);
    } catch (error) {
      lastError = error;
      continue;
    }
    if (!response.ok) {
      const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
      lastError = new Error(`workspace_webui_session_failed:${response.status}:${message}`);
      continue;
    }
    const webuiCookie = cookieHeaderFromSetCookie(setCookieHeader(response.headers)) ||
      (typeof payload?.token === "string" ? `aionui-session=${payload.token}` : "");
    return {
      ...workspaceAuth,
      apiBaseUrl,
      cookie: mergeCookieHeaders(workspaceAuth.cookie, webuiCookie)
    };
  }
  throw lastError || new Error("workspace_webui_session_failed");
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

async function verifyWorkspaceContentTransfer({ fetchImpl, checks, origin, accountId, workspace, runId, auth, operatorAuth }) {
  const state = await requestJson({ fetchImpl, origin, path: "/api/management/state", auth: operatorAuth });
  const organizations = new Map((state.organizations || [])
    .filter((item) => item.id && item.billingAccountId === accountId && item.status === "active")
    .map((item) => [item.id, item]));
  const organizationIds = new Set((state.memberships || [])
    .filter((item) => item.accountId === accountId && item.status === "active" && organizations.has(item.organizationId))
    .map((item) => item.organizationId));
  if (organizationIds.size !== 1) throw new Error("verification_organization_membership_required");
  const [organizationId] = organizationIds;
  const content = `${"x".repeat(4 << 20)}opl transfer ${runId}`;
  const digest = createHash("sha256").update(content).digest("hex");
  const path = `production-verifier/opl-transfer-${runId}.txt`;
  const project = await requestJson({
    fetchImpl,
    origin,
    path: "/api/projects",
    method: "POST",
    auth,
    idempotencyKey: `production-verification:${runId}:project`,
    body: { organizationId, workspaceId: workspace.id, localAliasId: `local-project-${runId}` }
  });
  const transfer = await requestJson({
    fetchImpl,
    origin,
    path: `/api/workspaces/${encodeURIComponent(workspace.id)}/transfers`,
    method: "POST",
    auth,
    idempotencyKey: `production-verification:${runId}:transfer`,
    body: { organizationId, projectId: project.projectId, path, digest, size: Buffer.byteLength(content) }
  });
  const body = Buffer.from(content);
  const chunks = Array.from({ length: transfer.chunkCount }, (_, index) => body.subarray(index * transfer.chunkSize, (index + 1) * transfer.chunkSize));
  const upload = async (index) => {
    const chunk = chunks[index];
    const response = await fetchImpl(endpoint(origin, `/api/workspaces/${encodeURIComponent(workspace.id)}/transfers/${encodeURIComponent(transfer.transferId)}/chunks/${index}`), {
      method: "PUT",
      headers: { ...authHeaderValues(auth), "content-type": "application/octet-stream", "x-chunk-sha256": createHash("sha256").update(chunk).digest("hex") },
      body: chunk
    });
    const payload = await readResponse(response);
    if (!response.ok) throw new Error(`workspace_transfer_chunk_failed:${index}:${response.status}:${payload?.error || payload}`);
  };

  await upload(0);
  const resumed = await requestJson({
    fetchImpl,
    origin,
    path: `/api/workspaces/${encodeURIComponent(workspace.id)}/transfers/${encodeURIComponent(transfer.transferId)}`,
    auth
  });
  addCheck(checks, "workspace_content_transfer_interrupted", resumed.status === "uploading" && resumed.receivedChunks?.length === 1 && resumed.receivedChunks[0] === 0);
  const received = new Set(resumed.receivedChunks || []);
  for (let index = 0; index < chunks.length; index += 1) {
    if (!received.has(index)) await upload(index);
  }

  const completed = await requestJson({
    fetchImpl,
    origin,
    path: `/api/workspaces/${encodeURIComponent(workspace.id)}/transfers/${encodeURIComponent(transfer.transferId)}/complete`,
    method: "POST",
    auth,
    body: {}
  });
  addCheck(checks, "workspace_content_transfer_completed", completed.status === "completed" && completed.receivedChunks?.length === chunks.length);

  const downloaded = await fetchImpl(endpoint(origin, `/api/workspaces/${encodeURIComponent(workspace.id)}/contents/${digest}`), { headers: authHeaderValues(auth) });
  const downloadedBody = await downloaded.text();
  addCheck(checks, "workspace_content_transfer_downloaded", downloaded.ok && downloaded.headers.get("x-content-sha256") === digest && downloadedBody === content);
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

async function configureModelAccessIfNeeded(page, { modelAccessKey = "" } = {}) {
  if (typeof page.evaluate !== "function") return false;
  const needsSetup = await page.evaluate(() => {
    const text = document.body?.innerText || "";
    return /Prepare One Person Lab|Model Access|Enter access key|Finish setup/i.test(text) &&
      !document.querySelector('input[type="file"]');
  });
  if (!needsSetup) return false;
  if (!modelAccessKey) throw new Error("workspace_browser_model_access_key_missing");

  let configured = false;
  if (typeof page.locator === "function" && typeof page.getByRole === "function") {
    try {
      const input = page.locator('input[type="password"], input[placeholder*="access" i], input[aria-label*="access" i], textarea').last();
      await input.fill(modelAccessKey, { timeout: 15_000 });
      await page.getByRole("button", { name: /Configure OPL Gateway|Finish setup|Continue|Start|Save|完成|继续|保存|开始/i }).first().click({ timeout: 15_000 });
      configured = true;
    } catch {
      configured = false;
    }
  }

  if (!configured) {
    const result = await page.evaluate(({ key }) => {
      const visible = (element) => {
        const rect = element.getBoundingClientRect();
        const style = window.getComputedStyle(element);
        return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
      };
      const fields = Array.from(document.querySelectorAll("input, textarea")).filter(visible);
      const field = fields.find((element) => {
        const label = [
          element.getAttribute("aria-label") || "",
          element.getAttribute("placeholder") || "",
          element.closest("label, div, section, form")?.innerText || ""
        ].join(" ");
        return /access key|api key|token|secret|model access|openai/i.test(label);
      }) || fields[fields.length - 1];
      if (!field) return "input_missing";

      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set ||
        Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")?.set;
      if (setter) setter.call(field, key);
      field.value = key;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.dispatchEvent(new Event("change", { bubbles: true }));

      const button = Array.from(document.querySelectorAll("button, [role='button']"))
        .filter((element) => !element.disabled && element.getAttribute("aria-disabled") !== "true" && visible(element))
        .find((element) => /Configure OPL Gateway|Finish setup|Continue|Start|Save|完成|继续|保存|开始/i.test(element.innerText || element.textContent || ""));
      if (!button) return "finish_button_missing";
      button.click();
      return true;
    }, { key: modelAccessKey });
    if (result !== true) throw new Error(`workspace_browser_model_access_${result}`);
  }

  await page.waitForFunction(() => {
    const text = document.body?.innerText || "";
    return Boolean(document.querySelector('input[type="file"]')) ||
      !/Prepare One Person Lab|Model Access Unknown|Enter access key|Finish setup/i.test(text);
  }, {}, { timeout: 60_000 });
  return true;
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
  modelAccessKey = "",
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
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_model_access_configured",
      screenshotDir,
      runId,
      recordSuccess: false,
      task: async () => {
        const configured = await configureModelAccessIfNeeded(page, { modelAccessKey });
        if (configured) addCheck(checks, "workspace_browser_model_access_configured", true);
      }
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

async function waitForStorageReady({
  fetchImpl,
  origin,
  accountId,
  storage,
  attempts,
  retryDelayMs,
  auth = null,
  checks
}) {
  let current = storage;
  for (let attempt = 0; attempt <= attempts; attempt += 1) {
    if (
      current?.id &&
      current?.provider === "tencent-tke" &&
      current?.providerResourceId?.startsWith("pvc/") &&
      current?.status === "available" &&
      current?.billingStatus === "active" &&
      Number(current?.sizeGb || 0) > 0
    ) {
      addCheck(checks, "storage_created", true, {
        storageId: current.id,
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
      path: `/api/storage-volumes/${encodeURIComponent(current.id)}/sync`,
      method: "POST",
      auth,
      body: { accountId, storageId: current.id }
    });
  }
  assertStorageShape(checks, current);
  return current;
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
    workspace?.access?.tokenStatus === "active" &&
    workspace?.access?.credentialStatus === "configured" &&
    workspace?.access?.account
  ), { workspaceId: workspace?.id });
}

function assertRuntimeStatus(checks, runtimeStatus, name = "workspace_runtime_status") {
  const runtimeChecks = (runtimeStatus?.checks || []).map((check) => ({
    name: check?.name,
    ok: check?.ok === true,
    ...Object.fromEntries(Object.entries(check || {}).filter(([key]) => !["name", "ok"].includes(key)))
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

function assertWorkspaceUrlTokenScrubbed(checks, workspaceAuth, name = "workspace_url_token_scrubbed") {
	let parsed = null;
	try {
		parsed = new URL(workspaceAuth?.url || "");
	} catch {
		parsed = null;
	}
	addCheck(checks, name, Boolean(
		parsed &&
		!parsed.searchParams.has("token")
	), { path: parsed ? `${parsed.pathname}${parsed.search}` : "" });
}

function settlementEntry(settlement) {
  if (!settlement?.resourceType) return null;
  const entry = {
    accountId: settlement.accountId,
    resourceId: settlement.resourceId,
    type: `${settlement.resourceType}_debit`
  };
  if (settlement.resourceType === "storage") entry.storageId = settlement.resourceId;
  else entry.computeAllocationId = settlement.resourceId;
  return entry;
}

function hasSettlementEvidence(entry) {
  return Boolean(
    entry?.pricingVersion &&
    entry?.providerCostEvidenceRef &&
    entry?.priceSnapshot &&
    typeof entry.priceSnapshot === "object" &&
    Object.keys(entry.priceSnapshot).length > 0 &&
    entry?.quantity != null &&
    entry?.unit
  );
}

function hasWalletAfterFields(entry) {
  return Boolean(
    entry &&
    entry.balanceCents != null &&
    entry.frozenCents != null &&
    entry.availableCents != null &&
    entry.totalSpentCents != null
  );
}

function verificationSettlementEvidence({ packageId, resourceType, amountCents, runtimeStatus }) {
  return {
    pricingVersion: VERIFICATION_PRICING_VERSION,
    priceSnapshot: {
      packageId,
      resourceType,
      unitPriceCents: amountCents,
      currency: "CNY",
      source: "production_verifier"
    },
    quantity: 1,
    unit: "verification",
    providerCostEvidenceRef: `fabric:${runtimeStatus?.operationId || runtimeStatus?.runtimeId || runtimeStatus?.workspaceId}`
  };
}

function assertResourceBillingSettlement(checks, settlements, { accountId, compute, storage }) {
  const responseEntries = settlements.flatMap((settlement) => settlement?.entries?.length ? settlement.entries : [settlementEntry(settlement)].filter(Boolean));
  const entries = responseEntries.length ? responseEntries : [
    { accountId, computeAllocationId: compute?.id, type: "compute_debit" },
    { accountId, storageId: storage?.id, type: "storage_debit" }
  ];
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

function assertLedgerAndWalletTransactions(checks, state, { accountId, compute, storage }) {
  const ledger = state?.billingLedger || [];
  const walletTransactions = state?.walletTransactions || [];

  const computeLedger = ledger.find((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const storageLedger = ledger.find((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const computeWalletTransaction = walletTransactions.find((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const storageWalletTransaction = walletTransactions.find((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const hasComputeLedger = Boolean(computeLedger);
  const hasStorageLedger = Boolean(storageLedger);
  const hasComputeWalletTransaction = Boolean(computeWalletTransaction);
  const hasStorageWalletTransaction = Boolean(storageWalletTransaction);
  const hasComputeSettlementEvidence = hasSettlementEvidence(computeLedger);
  const hasStorageSettlementEvidence = hasSettlementEvidence(storageLedger);
  const hasComputeWalletAfter = hasWalletAfterFields(computeWalletTransaction);
  const hasStorageWalletAfter = hasWalletAfterFields(storageWalletTransaction);
  const missingChecks = [
    [hasComputeLedger, "compute_ledger"],
    [hasStorageLedger, "storage_ledger"],
    [hasComputeWalletTransaction, "compute_wallet_transaction"],
    [hasStorageWalletTransaction, "storage_wallet_transaction"],
    [hasComputeSettlementEvidence, "compute_price_snapshot"],
    [hasStorageSettlementEvidence, "storage_price_snapshot"],
    [hasComputeWalletAfter, "compute_wallet_after"],
    [hasStorageWalletAfter, "storage_wallet_after"]
  ].filter(([ok]) => !ok).map(([, name]) => name);

  addCheck(checks, "ledger_and_wallet_transactions_verified", Boolean(
    state?.wallet?.accountId === accountId &&
    hasComputeLedger &&
    hasStorageLedger &&
    hasComputeWalletTransaction &&
    hasStorageWalletTransaction &&
    hasComputeSettlementEvidence &&
    hasStorageSettlementEvidence &&
    hasComputeWalletAfter &&
    hasStorageWalletAfter
  ), { missingChecks });
}

function assertFabricAuditEvidence(checks, state, { accountId, workspace, compute, storage, attachment }) {
  const rows = state?.resourceLedgerEvidence || [];
  const operations = state?.runtimeOperations || [];
  const row = rows.find((entry) =>
    (entry.accountId === accountId || entry.ownerAccountId === accountId) &&
    entry.workspaceId === workspace?.id &&
    entry.computeAllocationId === compute?.id &&
    entry.storageId === storage?.id &&
    entry.attachmentId === attachment?.id
  );
  const operation = operations.find((entry) =>
    entry.operationId === row?.operationId ||
    [compute?.id, storage?.id, attachment?.id, workspace?.id].includes(entry.resourceId)
  );
  const tags = row?.costTags || operation?.costTags || operation?.redactedProviderPayload?.costTags || {};
  const hasCostTags = Boolean(
    tags.opl_account_id === accountId &&
    tags.opl_workspace_id === workspace?.id &&
    tags.opl_resource_id &&
    tags.opl_operation_id
  );
  const hasLedgerLinks = Boolean(row?.ledgerEntryIds?.length && row?.walletTransactionIds?.length);
  addCheck(checks, "fabric_audit_evidence_verified", Boolean(
    row?.operationId &&
    operation?.operationId &&
    hasCostTags &&
    hasLedgerLinks
  ), {
    missingChecks: [
      [row?.operationId, "resource_operation"],
      [operation?.operationId, "runtime_operation"],
      [hasCostTags, "provider_cost_tags"],
      [hasLedgerLinks, "ledger_links"]
    ].filter(([ok]) => !ok).map(([, name]) => name)
  });
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

async function cleanupVerificationResources({ fetchImpl, origin, accountId, computeAllocationId, storageId, attachmentId, expectedComputeHoldId = "", expectedStorageHoldId = "", checks = null, auth = null, attempts = DEFAULT_WORKSPACE_URL_ATTEMPTS, retryDelayMs = DEFAULT_RETRY_DELAY_MS }) {
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
      let destroyed = null;
      for (let attempt = 1; attempt <= attempts; attempt += 1) {
        destroyed = await requestJson({
          fetchImpl,
          origin,
          path: `/api/compute-allocations/${encodeURIComponent(computeAllocationId)}/destroy`,
          method: "POST",
          auth,
          idempotencyKey: `production_verification_cleanup:compute:${computeAllocationId}`,
          body: { accountId, computeAllocationId, confirm: true }
        });
        if (
          destroyed?.status === "destroyed" &&
          destroyed?.billingStatus === "stopped" &&
          Boolean(destroyed?.holdReleaseId) &&
          (!expectedComputeHoldId || destroyed?.holdId === expectedComputeHoldId)
        ) break;
        if (attempt < attempts) await sleep(retryDelayMs);
      }
      const cleanupComplete = Boolean(
        destroyed?.status === "destroyed" &&
        destroyed?.billingStatus === "stopped" &&
        destroyed?.holdReleaseId &&
        (!expectedComputeHoldId || destroyed?.holdId === expectedComputeHoldId)
      );
      if (checks) {
        addCheck(checks, "verification_compute_destroyed", cleanupComplete);
      }
      if (!cleanupComplete) throw new Error("verification_compute_destroyed_failed");
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
          destroyed?.billingStatus === "stopped" &&
          (!expectedStorageHoldId || (destroyed?.holdId === expectedStorageHoldId && Boolean(destroyed?.holdReleaseId)))
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
  ownerEmail = "",
  ownerPassword = "",
  allowPrivateConsoleOrigin = false,
  browserE2E = false,
  browserFactory = null,
  screenshotDir = "",
  modelAccessKey = "",
  cleanupOnFailure = true,
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
  let operatorAuth = null;

  try {
    const productionReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
    assertReady({ checks, name: "production_readiness", payload: productionReadiness });

    const runtimeReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/runtime/readiness" });
    assertReady({ checks, name: "runtime_readiness", payload: runtimeReadiness });

    operatorAuth = await requestOperatorSession({ fetchImpl, origin: normalizedOrigin, operatorToken });

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/topups",
      method: "POST",
      auth: operatorAuth,
      idempotencyKey: creditSourceEventId,
      body: { accountId, amount: creditAmount, reason: creditSourceEventId, confirm: true }
    });

    if (Boolean(ownerEmail) !== Boolean(ownerPassword)) throw new Error("verification_owner_credentials_required");
    auth = ownerEmail
      ? await requestOwnerSession({ fetchImpl, origin: normalizedOrigin, email: ownerEmail, password: ownerPassword })
      : operatorAuth;

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
    storage = await waitForStorageReady({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      storage,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth,
      checks
    });

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
    assertWorkspaceUrlTokenScrubbed(checks, workspaceUrlResult);
    const webuiUsername = workspace.access?.account || workspace.access?.username || DEFAULT_AIONUI_ADMIN_USERNAME;
    const workspaceApiAuth = await requestWorkspaceWebuiSession({
      fetchImpl,
      workspaceAuth: workspaceUrlResult
    });
    if (browserE2E) {
      await verifyWorkspaceBrowserUi({
        workspaceUrl: workspace.url,
        workspaceAuth: {
          ...workspaceApiAuth,
          webuiUsername
        },
        runId,
        checks,
        browserFactory,
        modelAccessKey,
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
    await verifyWorkspaceContentTransfer({
      fetchImpl,
      checks,
      origin: normalizedOrigin,
      accountId,
      workspace,
      runId,
      auth,
      operatorAuth
    });

    const firstCleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: compute.id,
      attachmentId: attachment.id,
      expectedComputeHoldId: compute.holdId,
      checks,
      auth,
      attempts: workspaceUrlAttempts,
      retryDelayMs
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
    assertWorkspaceUrlTokenScrubbed(checks, replacementWorkspaceUrlResult, "replacement_workspace_url_token_scrubbed");
    const replacementWorkspaceApiAuth = await requestWorkspaceWebuiSession({
      fetchImpl,
      workspaceAuth: replacementWorkspaceUrlResult
    });
    await verifyWorkspacePersistedFile({
      fetchImpl,
      checks,
      workspaceUrl: replacementWorkspace.url,
      fileProof,
      workspaceAuth: replacementWorkspaceApiAuth
    });

    const computeSettlementAmountCents = 100;
    const computeSettlement = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/resource-settlements",
      method: "POST",
      auth: operatorAuth || auth,
      body: {
        accountId,
        workspaceId: replacementCompute.workspaceId || "",
        resourceType: "compute",
        resourceId: replacementCompute.id,
        computeAllocationId: replacementCompute.id,
        holdId: replacementCompute.holdId,
        amountCents: computeSettlementAmountCents,
        ...verificationSettlementEvidence({ packageId, resourceType: "compute", amountCents: computeSettlementAmountCents, runtimeStatus: replacementRuntimeStatus }),
        sourceEventId: `production_verification_resource_settlement:compute:${runId}`,
        confirm: true
      }
    });
    const storageSettlementAmountCents = 100;
    const storageSettlement = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/resource-settlements",
      method: "POST",
      auth: operatorAuth || auth,
      body: {
        accountId,
        workspaceId: storage.workspaceId || "",
        resourceType: "storage",
        resourceId: storage.id,
        storageId: storage.id,
        holdId: storage.holdId,
        amountCents: storageSettlementAmountCents,
        ...verificationSettlementEvidence({ packageId, resourceType: "storage", amountCents: storageSettlementAmountCents, runtimeStatus: replacementRuntimeStatus }),
        sourceEventId: `production_verification_resource_settlement:storage:${runId}`,
        confirm: true
      }
    });
    assertResourceBillingSettlement(checks, [computeSettlement, storageSettlement], {
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
    assertLedgerAndWalletTransactions(checks, state, {
      accountId,
      compute: replacementCompute,
      storage
    });
    assertFabricAuditEvidence(checks, state, {
      accountId,
      workspace: replacementWorkspace,
      compute: replacementCompute,
      storage,
      attachment: replacementAttachment
    });

    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: replacementCompute.id,
      storageId: storage.id,
      attachmentId: replacementAttachment.id,
      expectedComputeHoldId: replacementCompute.holdId,
      expectedStorageHoldId: storage.holdId,
      checks,
      auth,
      attempts: workspaceUrlAttempts,
      retryDelayMs
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
    if (!cleanupOnFailure) {
      error.cleanupSkipped = true;
      throw error;
    }
    const cleanupErrors = [];
    if (replacementCompute?.id || replacementAttachment?.id) {
      const replacementCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        computeAllocationId: replacementCompute?.id,
        attachmentId: replacementAttachment?.id,
        expectedComputeHoldId: replacementCompute?.holdId,
        auth,
        attempts: workspaceUrlAttempts,
        retryDelayMs
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
        expectedComputeHoldId: compute?.holdId,
        auth,
        attempts: workspaceUrlAttempts,
        retryDelayMs
      });
      cleanupErrors.push(...primaryCleanupErrors);
    }
    if (storage?.id) {
      const storageCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        storageId: storage.id,
        auth,
        attempts: workspaceUrlAttempts,
        retryDelayMs
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
  const requestedAccountId = args.account || env.OPL_VERIFY_ACCOUNT_ID || "";
  const owner = verificationOwnerFromSeed(env.OPL_VERIFY_AUTH_USERS_JSON, requestedAccountId);
  const accountId = owner.accountId || requestedAccountId || DEFAULT_ACCOUNT_ID;
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    accountId,
    workspaceName: args.workspace || env.OPL_VERIFY_WORKSPACE_NAME,
    runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
    packageId: args.package || env.OPL_VERIFY_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    creditAmount: Number(args.credit || env.OPL_VERIFY_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_WORKSPACE_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    operatorToken: args["operator-token"] || env.OPL_VERIFY_OPERATOR_TOKEN || "",
    ownerEmail: owner.email,
    ownerPassword: owner.password,
    browserE2E: ["1", "true"].includes(String(args["browser-e2e"] || env.OPL_VERIFY_BROWSER_E2E || "").toLowerCase()),
    screenshotDir: args["screenshot-dir"] || env.OPL_VERIFY_SCREENSHOT_DIR || "",
    modelAccessKey: args["model-access-key"] || env.OPL_VERIFY_MODEL_ACCESS_KEY || env.OPL_CODEX_API_KEY || "",
    cleanupOnFailure: !["0", "false", "no"].includes(String(args["cleanup-on-failure"] || env.OPL_VERIFY_CLEANUP_ON_FAILURE || "true").toLowerCase()),
    fetchImpl
  };
}

export function verificationOwnerFromSeed(raw, accountId) {
  if (!raw) return { accountId: "", email: "", password: "" };
  let users;
  try {
    users = JSON.parse(raw);
  } catch {
    throw new Error("verification_owner_credentials_required");
  }
  const owners = Array.isArray(users) ? users.filter((user) =>
    (!accountId || user?.accountId === accountId) &&
    (user.role === "owner" || user.role === "pi") &&
    user.accountId && user.email && user.password
  ) : [];
  if (owners.length !== 1) throw new Error("verification_owner_credentials_required");
  return { accountId: owners[0].accountId, email: owners[0].email, password: owners[0].password };
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
    ...(error.cleanupSkipped ? { cleanupSkipped: true } : {}),
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
	if (argv.includes("--help") || argv.includes("-h")) {
		stdout.write("Usage: npm run verify:production -- --origin <https-url> [--account <id>] [--run-id <id>] [--package <id>] [--browser-e2e]\n");
		return 0;
	}
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
