<script setup lang="ts">
import {
  BookOpen,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Columns3,
  Eye,
  EyeOff,
  MoreHorizontal,
  Plus,
  RefreshCw,
  X
} from "@lucide/vue";
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";

import {
  createGatewayKey,
  deleteGatewayKey,
  getGatewayEndpoint,
  getGatewayGroups,
  getGatewayKey,
  getGatewayKeys,
  revealGatewayKey,
  updateGatewayKey
} from "../../api/console-read-api.ts";
import type {
  CreateGatewayKeyRequest,
  GatewayEndpointDTO,
  GatewayGroupDTO,
  GatewayGroupPageDTO,
  GatewayKeyListQuery,
  GatewayKeyPageDTO,
  GatewayKeySecretDTO,
  GatewayKeySummaryDTO,
  SourceEnvelope,
  UpdateGatewayKeyRequest
} from "../../api/dtos.ts";
import { formatDate, formatUsdMicros } from "../../console-model.ts";
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCheckbox,
  UiCodeBlock,
  UiCopyButton,
  UiEmptyState,
  UiIndicator,
  UiInput,
  UiMenu,
  UiPopover,
  UiSelect,
  UiTextarea,
  UiTooltip
} from "../ui/index.ts";

const props = defineProps<{ csrfToken: string }>();

type Dialog = "" | "key" | "delete" | "use";
type Column = "group" | "status" | "quota" | "rate" | "expires" | "lastUsed" | "created";
type KeyMenuAction = "reveal" | "use" | "edit" | "toggle" | "reset-quota" | "reset-rate" | "delete";
const reservedWorkspaceKeyName = "opl-workspace";
const columnOptions: { key: Column; label: string }[] = [
  { key: "group", label: "分组" },
  { key: "status", label: "状态" },
  { key: "quota", label: "配额" },
  { key: "rate", label: "消费限额" },
  { key: "expires", label: "过期" },
  { key: "lastUsed", label: "最近使用" },
  { key: "created", label: "创建时间" }
];

const source = ref<SourceEnvelope<GatewayKeyPageDTO> | null>(null);
const groupsSource = ref<SourceEnvelope<GatewayGroupPageDTO> | null>(null);
const endpointSource = ref<SourceEnvelope<GatewayEndpointDTO> | null>(null);
const loading = ref(false);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const dialog = ref<Dialog>("");
const dialogRoot = ref<HTMLElement | null>(null);
const editingKey = ref<GatewayKeySummaryDTO | null>(null);
const pendingDelete = ref<GatewayKeySummaryDTO | null>(null);
const useKey = ref<GatewayKeySummaryDTO | null>(null);
const revealed = ref<GatewayKeySecretDTO | null>(null);
const columnsOpen = ref(false);
const mobileFilters = ref(window.matchMedia("(max-width: 820px)").matches);
const filtersOpen = ref(!mobileFilters.value);
let mobileFiltersMedia: MediaQueryList | null = null;
const requestGeneration = ref(0);
const query = reactive<Required<Omit<GatewayKeyListQuery, "groupId">> & { groupId: string }>({
  page: 1,
  pageSize: 20,
  search: "",
  status: "",
  groupId: "",
  sortBy: "createdAt",
  sortOrder: "desc"
});
const visible = reactive<Record<Column, boolean>>({
  group: true,
  status: true,
  quota: true,
  rate: true,
  expires: true,
  lastUsed: true,
  created: true
});
const form = reactive({
  name: "",
  groupId: "",
  quotaUsd: 0,
  ipWhitelist: "",
  ipBlacklist: "",
  expiresInDays: 30,
  expiresAt: "",
  rateLimit5hUsd: 0,
  rateLimit1dUsd: 0,
  rateLimit7dUsd: 0
});

let secretTimer: number | undefined;
let sessionGeneration = 0;
let dialogReturnFocus: HTMLElement | null = null;
let createIntent: { input: CreateGatewayKeyRequest; key: string } | null = null;
const updateIntents = new Map<string, { signature: string; key: string }>();
const deleteIntents = new Map<string, string>();

const groups = computed(() => groupsSource.value?.available ? groupsSource.value.data.items : []);
const keys = computed(() => source.value?.available ? source.value.data.items : []);
const endpoint = computed(() => endpointSource.value?.available ? endpointSource.value.data.baseUrl : "");
const pages = computed(() => source.value?.available ? source.value.data.pages : 0);
const total = computed(() => source.value?.available ? source.value.data.total : 0);
const columnCount = computed(() => 2 + Object.values(visible).filter(Boolean).length);
const groupPlatform = computed(() => groups.value.find((group) => group.id === useKey.value?.groupId)?.platform || "");
const useConfiguration = computed(() => {
  if (!useKey.value || revealed.value?.id !== useKey.value.id || !revealed.value.value || !endpoint.value || !groupPlatform.value) return "";
  return JSON.stringify({ platform: groupPlatform.value, baseURL: endpoint.value, apiKey: revealed.value.value }, null, 2);
});

function updateMobileFilters(event: MediaQueryListEvent) {
  mobileFilters.value = event.matches;
  if (!event.matches) filtersOpen.value = true;
}

function onFiltersToggle(event: Event) {
  filtersOpen.value = (event.currentTarget as HTMLDetailsElement).open;
}

function friendlyError(value: unknown) {
  const message = value instanceof Error ? value.message : String(value || "");
  if (/upstream_unavailable|failed to fetch|networkerror/i.test(message)) return "服务暂不可用，请稍后重试";
  return message || "请求失败，请稍后重试";
}

function apiErrorCode(value: unknown) {
  const payload = value && typeof value === "object" && "payload" in value
    ? (value as { payload?: unknown }).payload
    : null;
  return payload && typeof payload === "object" ? String((payload as { error?: unknown }).error || "") : "";
}

function currentSessionRequest() {
  const generation = sessionGeneration;
  const csrfToken = props.csrfToken;
  return () => generation === sessionGeneration && csrfToken === props.csrfToken;
}

function parseLines(value: string) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function usdMicros(value: number) {
  const micros = Math.round(value * 1_000_000);
  if (!Number.isSafeInteger(micros) || micros < 0) throw new Error("金额格式无效");
  return micros;
}

function idempotencyKey(prefix: string) {
  return `${prefix}:${crypto.randomUUID()}`;
}

function clearSecret() {
  revealed.value = null;
  if (secretTimer !== undefined) window.clearTimeout(secretTimer);
  secretTimer = undefined;
}

function clearKeyState() {
  requestGeneration.value++;
  clearSecret();
  source.value = null;
  groupsSource.value = null;
  endpointSource.value = null;
  loading.value = false;
  busy.value = false;
  error.value = "";
  notice.value = "";
  dialog.value = "";
  editingKey.value = null;
  pendingDelete.value = null;
  useKey.value = null;
  createIntent = null;
  updateIntents.clear();
  deleteIntents.clear();
}

function armSecretTimer() {
  if (secretTimer !== undefined) window.clearTimeout(secretTimer);
  secretTimer = window.setTimeout(clearSecret, 60_000);
}

async function refreshReferenceData() {
  const requestStillCurrent = currentSessionRequest();
  const [groupResult, endpointResult] = await Promise.allSettled([getGatewayGroups(), getGatewayEndpoint()]);
  if (!requestStillCurrent()) return;
  groupsSource.value = groupResult.status === "fulfilled" ? groupResult.value : null;
  endpointSource.value = endpointResult.status === "fulfilled" ? endpointResult.value : null;
}

async function loadKeys(resetPage = false) {
  const requestStillCurrent = currentSessionRequest();
  if (resetPage) query.page = 1;
  const generation = ++requestGeneration.value;
  loading.value = true;
  error.value = "";
  clearSecret();
  try {
    const result = await getGatewayKeys({ ...query });
    if (generation !== requestGeneration.value || !requestStillCurrent()) return;
    source.value = result;
    if (result.available && result.data.page !== query.page) throw new Error("gateway_key_page_mismatch");
  } catch (value) {
    if (generation !== requestGeneration.value || !requestStillCurrent()) return;
    source.value = null;
    error.value = friendlyError(value);
  } finally {
    if (generation === requestGeneration.value && requestStillCurrent()) loading.value = false;
  }
}

async function refreshAll() {
  await Promise.all([refreshReferenceData(), loadKeys()]);
}

function groupName(groupId: string | null) {
  const group = groups.value.find((item) => item.id === groupId);
  return group?.name || "未分组";
}

function groupMetadataLabel(group: GatewayGroupDTO) {
  const status = group.status === "active" ? "可用" : group.status === "disabled" ? "停用" : group.status;
  return `${group.platform} · ${group.rateMultiplier}x · ${status}`;
}

function groupMeta(groupId: string | null) {
  const group = groups.value.find((item) => item.id === groupId);
  return group ? groupMetadataLabel(group) : "";
}

function groupOptionLabel(group: GatewayGroupDTO) {
  return `${group.name} · ${groupMetadataLabel(group)}`;
}

function statusLabel(status: GatewayKeySummaryDTO["status"]) {
  return { active: "启用", disabled: "停用", quota_exhausted: "额度用尽", expired: "已过期" }[status];
}

function isProtectedWorkspaceKey(key: GatewayKeySummaryDTO) {
  return key.kind === "workspace" || key.name === reservedWorkspaceKeyName;
}

function canManage(key: GatewayKeySummaryDTO) {
  return key.manageable && !isProtectedWorkspaceKey(key);
}

function canDelete(key: GatewayKeySummaryDTO) {
  return key.deletable && !isProtectedWorkspaceKey(key);
}

function keyMenuItems(key: GatewayKeySummaryDTO) {
  const manageable = canManage(key);
  return [
    { id: "reveal", label: revealed.value?.id === key.id ? "隐藏 Key" : "显示 Key", disabled: busy.value },
    { id: "use", label: "使用说明", disabled: busy.value },
    { id: "edit", label: "编辑", disabled: busy.value || !manageable, separatorBefore: true },
    { id: "toggle", label: key.status === "active" ? "停用" : "启用", disabled: busy.value || !manageable },
    { id: "reset-quota", label: "重置配额", disabled: busy.value || !manageable },
    { id: "reset-rate", label: "重置消费限额", disabled: busy.value || !manageable },
    { id: "delete", label: "删除", color: "danger" as const, disabled: busy.value || !canDelete(key), separatorBefore: true }
  ];
}

function keySecondaryMenuItems(key: GatewayKeySummaryDTO) {
  const manageable = canManage(key);
  return [
    { id: "edit", label: "编辑", disabled: busy.value || !manageable },
    { id: "toggle", label: key.status === "active" ? "停用" : "启用", disabled: busy.value || !manageable },
    { id: "reset-quota", label: "重置配额", disabled: busy.value || !manageable, separatorBefore: true },
    { id: "reset-rate", label: "重置消费限额", disabled: busy.value || !manageable },
    { id: "delete", label: "删除", color: "danger" as const, disabled: busy.value || !canDelete(key), separatorBefore: true }
  ];
}

function keyQuotaProgress(key: GatewayKeySummaryDTO) {
  if (key.quotaUsdMicros <= 0) return null;
  return Math.min(100, Math.max(0, key.quotaUsedUsdMicros / key.quotaUsdMicros * 100));
}

function handleKeyMenu(key: GatewayKeySummaryDTO, action: string) {
  switch (action as KeyMenuAction) {
    case "reveal": void reveal(key); break;
    case "use": void openUse(key); break;
    case "edit": openEdit(key); break;
    case "toggle": toggleKey(key); break;
    case "reset-quota": resetQuota(key); break;
    case "reset-rate": resetRateLimit(key); break;
    case "delete": askDelete(key); break;
  }
}

function sameStrings(left: string[], right: string[]) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function keyMatchesCreate(key: GatewayKeySummaryDTO, input: CreateGatewayKeyRequest) {
  return key.name === input.name
    && key.groupId === input.groupId
    && sameStrings(key.ipWhitelist, input.ipWhitelist || [])
    && sameStrings(key.ipBlacklist, input.ipBlacklist || [])
    && key.quotaUsdMicros === input.quotaUsdMicros
    && key.rateLimit5hUsdMicros === (input.rateLimit5hUsdMicros || 0)
    && key.rateLimit1dUsdMicros === (input.rateLimit1dUsdMicros || 0)
    && key.rateLimit7dUsdMicros === (input.rateLimit7dUsdMicros || 0);
}

function keyMatchesUpdate(key: GatewayKeySummaryDTO, input: UpdateGatewayKeyRequest) {
  if (input.name !== undefined && key.name !== input.name) return false;
  if (input.groupId !== undefined && key.groupId !== input.groupId) return false;
  if (input.ipWhitelist !== undefined && !sameStrings(key.ipWhitelist, input.ipWhitelist)) return false;
  if (input.ipBlacklist !== undefined && !sameStrings(key.ipBlacklist, input.ipBlacklist)) return false;
  if (input.quotaUsdMicros !== undefined && key.quotaUsdMicros !== input.quotaUsdMicros) return false;
  if (input.rateLimit5hUsdMicros !== undefined && key.rateLimit5hUsdMicros !== input.rateLimit5hUsdMicros) return false;
  if (input.rateLimit1dUsdMicros !== undefined && key.rateLimit1dUsdMicros !== input.rateLimit1dUsdMicros) return false;
  if (input.rateLimit7dUsdMicros !== undefined && key.rateLimit7dUsdMicros !== input.rateLimit7dUsdMicros) return false;
  if (input.expiresAt !== undefined && key.expiresAt !== (input.expiresAt ? new Date(input.expiresAt).toISOString() : null)) return false;
  if (input.enabled !== undefined && key.status !== (input.enabled ? "active" : "disabled")) return false;
  if (input.resetQuota && key.quotaUsedUsdMicros !== 0) return false;
  if (input.resetRateLimitUsage && (key.usage5hUsdMicros !== 0 || key.usage1dUsdMicros !== 0 || key.usage7dUsdMicros !== 0)) return false;
  return true;
}

function rememberDialogTrigger() {
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const menuTrigger = active?.closest(".ui-popover-root")?.querySelector<HTMLElement>('button[aria-haspopup="menu"]');
  dialogReturnFocus = menuTrigger || active;
}

function dialogControls() {
  return Array.from(dialogRoot.value?.querySelectorAll<HTMLElement>(
    'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])'
  ) || []).filter((element) => !element.hidden && element.getAttribute("aria-hidden") !== "true");
}

function onDialogKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.stopPropagation();
    event.preventDefault();
    closeDialog();
    return;
  }
  if (event.key === "Tab") {
    const controls = dialogControls();
    if (!controls.length) {
      event.preventDefault();
      return;
    }
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && (document.activeElement === first || !dialogRoot.value?.contains(document.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }
}

function openCreate() {
  rememberDialogTrigger();
  editingKey.value = null;
  Object.assign(form, {
    name: "",
    groupId: groups.value[0]?.id || "",
    quotaUsd: 0,
    ipWhitelist: "",
    ipBlacklist: "",
    expiresInDays: 30,
    expiresAt: "",
    rateLimit5hUsd: 0,
    rateLimit1dUsd: 0,
    rateLimit7dUsd: 0
  });
  dialog.value = "key";
}

function openEdit(key: GatewayKeySummaryDTO) {
  if (!canManage(key)) return;
  rememberDialogTrigger();
  editingKey.value = key;
  Object.assign(form, {
    name: key.name,
    groupId: key.groupId || "",
    quotaUsd: key.quotaUsdMicros / 1_000_000,
    ipWhitelist: key.ipWhitelist.join("\n"),
    ipBlacklist: key.ipBlacklist.join("\n"),
    expiresInDays: 30,
    expiresAt: key.expiresAt ? key.expiresAt.slice(0, 16) : "",
    rateLimit5hUsd: key.rateLimit5hUsdMicros / 1_000_000,
    rateLimit1dUsd: key.rateLimit1dUsdMicros / 1_000_000,
    rateLimit7dUsd: key.rateLimit7dUsdMicros / 1_000_000
  });
  dialog.value = "key";
}

function closeDialog() {
  if (busy.value) return;
  dialog.value = "";
  editingKey.value = null;
  pendingDelete.value = null;
  useKey.value = null;
}

function createRequest(): CreateGatewayKeyRequest {
  return {
    name: form.name.trim(),
    groupId: form.groupId,
    ipWhitelist: parseLines(form.ipWhitelist),
    ipBlacklist: parseLines(form.ipBlacklist),
    quotaUsdMicros: usdMicros(form.quotaUsd),
    expiresInDays: form.expiresInDays > 0 ? form.expiresInDays : undefined,
    rateLimit5hUsdMicros: usdMicros(form.rateLimit5hUsd),
    rateLimit1dUsdMicros: usdMicros(form.rateLimit1dUsd),
    rateLimit7dUsdMicros: usdMicros(form.rateLimit7dUsd)
  };
}

function updateRequest(): UpdateGatewayKeyRequest {
  return {
    name: form.name.trim(),
    groupId: form.groupId,
    ipWhitelist: parseLines(form.ipWhitelist),
    ipBlacklist: parseLines(form.ipBlacklist),
    quotaUsdMicros: usdMicros(form.quotaUsd),
    expiresAt: form.expiresAt ? new Date(form.expiresAt).toISOString() : "",
    rateLimit5hUsdMicros: usdMicros(form.rateLimit5hUsd),
    rateLimit1dUsdMicros: usdMicros(form.rateLimit1dUsd),
    rateLimit7dUsdMicros: usdMicros(form.rateLimit7dUsd)
  };
}

async function submitKey() {
  const requestStillCurrent = currentSessionRequest();
  if (busy.value || !form.name.trim() || !form.groupId || !props.csrfToken) return;
  busy.value = true;
  error.value = "";
  try {
    if (editingKey.value) {
      await mutateKey(editingKey.value, updateRequest(), "API Key 已更新");
      if (!requestStillCurrent()) return;
    } else {
      const input = createRequest();
      if (!createIntent || JSON.stringify(createIntent.input) !== JSON.stringify(input)) {
        createIntent = { input, key: idempotencyKey("key-create") };
      }
      const created = await createGatewayKey(input, props.csrfToken, createIntent.key);
      if (!requestStillCurrent()) return;
      if (!created.available) throw new Error("gateway_key_unavailable");
      const readback = await getGatewayKey(created.data.id);
      if (!requestStillCurrent()) return;
      if (!readback.available || readback.data.id !== created.data.id || !keyMatchesCreate(readback.data, input)) throw new Error("gateway_key_readback_unavailable");
      createIntent = null;
      await loadKeys(true);
      if (!requestStillCurrent()) return;
      const secret = await revealGatewayKey(created.data.id, props.csrfToken);
      if (!requestStillCurrent()) return;
      if (!secret.available || secret.data.id !== created.data.id || !secret.data.value) throw new Error("gateway_key_unavailable");
      revealed.value = secret.data;
      armSecretTimer();
      notice.value = "API Key 已创建";
      dialog.value = "";
    }
  } catch (value) {
    if (requestStillCurrent()) error.value = friendlyError(value);
  } finally {
    if (requestStillCurrent()) busy.value = false;
  }
}

async function mutateKey(key: GatewayKeySummaryDTO, input: UpdateGatewayKeyRequest, message: string) {
  const requestStillCurrent = currentSessionRequest();
  if (!canManage(key)) return;
  const signature = JSON.stringify(input);
  let intent = updateIntents.get(key.id);
  if (!intent || intent.signature !== signature) {
    intent = { signature, key: idempotencyKey("key-update") };
    updateIntents.set(key.id, intent);
  }
  const updated = await updateGatewayKey(key.id, input, props.csrfToken, intent.key);
  if (!requestStillCurrent()) return;
  if (!updated.available || !keyMatchesUpdate(updated.data, input)) throw new Error("gateway_key_unavailable");
  const readback = await getGatewayKey(key.id);
  if (!requestStillCurrent()) return;
  if (!readback.available || readback.data.id !== key.id || !keyMatchesUpdate(readback.data, input)) throw new Error("gateway_key_readback_unavailable");
  updateIntents.delete(key.id);
  notice.value = message;
  dialog.value = "";
  await loadKeys();
  if (!requestStillCurrent()) return;
}

async function runKeyMutation(key: GatewayKeySummaryDTO, input: UpdateGatewayKeyRequest, message: string) {
  const requestStillCurrent = currentSessionRequest();
  if (busy.value || !props.csrfToken) return;
  busy.value = true;
  error.value = "";
  try {
    await mutateKey(key, input, message);
    if (!requestStillCurrent()) return;
  } catch (value) {
    if (requestStillCurrent()) error.value = friendlyError(value);
  } finally {
    if (requestStillCurrent()) busy.value = false;
  }
}

function changeGroup(key: GatewayKeySummaryDTO, value: string | number) {
  const groupId = String(value);
  if (groupId && groupId !== key.groupId) void runKeyMutation(key, { groupId }, "分组已更新");
}

function toggleKey(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { enabled: key.status !== "active" }, key.status === "active" ? "API Key 已停用" : "API Key 已启用");
}

function resetQuota(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { resetQuota: true }, "配额用量已重置");
}

function resetRateLimit(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { resetRateLimitUsage: true }, "消费限额用量已重置");
}

async function reveal(key: GatewayKeySummaryDTO) {
  const requestStillCurrent = currentSessionRequest();
  if (busy.value || !props.csrfToken) return;
  if (revealed.value?.id === key.id) {
    clearSecret();
    return;
  }
  busy.value = true;
  error.value = "";
  clearSecret();
  try {
    // The Control Plane response is private, no-store and owner+CSRF audited.
    const result = await revealGatewayKey(key.id, props.csrfToken);
    if (!requestStillCurrent()) return;
    if (!result.available || !result.data.value) throw new Error("gateway_key_unavailable");
    revealed.value = result.data;
    armSecretTimer();
  } catch (value) {
    if (requestStillCurrent()) error.value = friendlyError(value);
  } finally {
    if (requestStillCurrent()) busy.value = false;
  }
}

function askDelete(key: GatewayKeySummaryDTO) {
  if (!canDelete(key)) return;
  rememberDialogTrigger();
  pendingDelete.value = key;
  dialog.value = "delete";
}

async function removeKey() {
  const key = pendingDelete.value;
  const requestStillCurrent = currentSessionRequest();
  if (!key || busy.value || !props.csrfToken) return;
  busy.value = true;
  const intent = deleteIntents.get(key.id) || idempotencyKey("key-delete");
  deleteIntents.set(key.id, intent);
  try {
    let deleteError: unknown = null;
    try {
      const result = await deleteGatewayKey(key.id, props.csrfToken, intent);
      if (!requestStillCurrent()) return;
      if (!result.available || result.data.status !== "deleted") deleteError = new Error("gateway_key_delete_unavailable");
    } catch (value) {
      if (!requestStillCurrent()) return;
      deleteError = value;
    }
    if (deleteError) {
      let missing = false;
      try {
        await getGatewayKey(key.id);
        if (!requestStillCurrent()) return;
      } catch (readError) {
        if (!requestStillCurrent()) return;
        missing = apiErrorCode(readError) === "gateway_key_not_found";
      }
      if (!missing) throw deleteError;
    }
    deleteIntents.delete(key.id);
    notice.value = "API Key 已删除";
    dialog.value = "";
    await loadKeys();
    if (!requestStillCurrent()) return;
  } catch (value) {
    if (requestStillCurrent()) error.value = friendlyError(value);
  } finally {
    if (requestStillCurrent()) busy.value = false;
  }
}

async function openUse(key: GatewayKeySummaryDTO) {
  rememberDialogTrigger();
  useKey.value = key;
  if (revealed.value?.id !== key.id) await reveal(key);
  if (revealed.value?.id === key.id) dialog.value = "use";
}

function changePage(page: number) {
  if (page < 1 || page > pages.value || page === query.page) return;
  query.page = page;
  void loadKeys();
}

watch(() => props.csrfToken, (value, previous) => {
  if (value === previous) return;
  sessionGeneration += 1;
  clearKeyState();
  if (value) void refreshAll();
});

watch(dialog, async (value, previous) => {
  if (value) {
    await nextTick();
    const autofocus = dialogRoot.value?.querySelector<HTMLElement>("[data-autofocus]");
    (autofocus || dialogControls()[0])?.focus();
    return;
  }
  if (!previous) return;
  const returnFocus = dialogReturnFocus;
  dialogReturnFocus = null;
  await nextTick();
  if (returnFocus?.isConnected) returnFocus.focus();
});

onMounted(() => {
  mobileFiltersMedia = window.matchMedia("(max-width: 820px)");
  mobileFiltersMedia.addEventListener("change", updateMobileFilters);
  if (props.csrfToken) void refreshAll();
});
onBeforeUnmount(() => {
  mobileFiltersMedia?.removeEventListener("change", updateMobileFilters);
  mobileFiltersMedia = null;
  sessionGeneration += 1;
  clearKeyState();
});
</script>

<template>
  <section class="keys-panel panel" :inert="Boolean(dialog)">
    <header class="keys-header">
      <div class="keys-header__copy">
        <h2>API Keys</h2>
        <div class="endpoint-line">
          <span>API Endpoint</span>
          <code v-if="endpoint">{{ endpoint }}</code>
          <span v-else class="muted">暂不可用</span>
          <UiCopyButton :value="endpoint" label="复制 Endpoint" :disabled="!endpoint" @copied="notice = 'Endpoint 已复制'" />
        </div>
      </div>
      <div class="header-actions">
        <UiTooltip text="刷新 API Keys"><UiButton variant="outline" color="secondary" size="sm" aria-label="刷新 API Keys" :disabled="loading" @click="refreshAll"><RefreshCw :size="17" /></UiButton></UiTooltip>
        <UiButton :disabled="!groups.length" @click="openCreate"><Plus :size="16" />创建 Key</UiButton>
      </div>
    </header>

    <details class="key-filter-disclosure" :open="!mobileFilters || filtersOpen" @toggle="onFiltersToggle">
      <summary>筛选与显示 <ChevronDown :size="16" aria-hidden="true" /></summary>
    <form class="key-filters key-filter-fields" @submit.prevent="loadKeys(true)">
      <UiInput v-model="query.search" label="搜索 Key" maxlength="100" placeholder="名称或 ID" />
      <UiSelect v-model="query.groupId" label="分组筛选" @change="loadKeys(true)"><option value="">全部分组</option><option value="0">未分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ groupOptionLabel(group) }}</option></UiSelect>
      <UiSelect v-model="query.status" label="状态筛选" @change="loadKeys(true)"><option value="">全部状态</option><option value="active">启用</option><option value="disabled">停用</option><option value="quota_exhausted">额度用尽</option><option value="expired">已过期</option></UiSelect>
      <UiSelect v-model="query.sortBy" label="排序" @change="loadKeys(true)"><option value="createdAt">创建时间</option><option value="name">名称</option><option value="id">ID</option><option value="currentConcurrency">当前并发</option><option value="expiresAt">过期时间</option><option value="status">状态</option><option value="lastUsedAt">最近使用</option></UiSelect>
      <UiSelect v-model="query.sortOrder" label="顺序" @change="loadKeys(true)"><option value="desc">降序</option><option value="asc">升序</option></UiSelect>
      <UiSelect v-model="query.pageSize" label="每页" @change="loadKeys(true)"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option></UiSelect>
      <UiButton class="filter-submit" type="submit" variant="outline" color="secondary">查询</UiButton>
      <div class="column-control">
        <UiPopover v-model="columnsOpen" label="列设置" position="bottom-end">
          <template #trigger="{ toggle }"><UiButton variant="outline" color="secondary" @click="toggle"><Columns3 :size="16" />列设置</UiButton></template>
          <div class="column-options"><UiCheckbox v-for="item in columnOptions" :key="item.key" v-model="visible[item.key]" :label="item.label" /></div>
        </UiPopover>
      </div>
    </form>
    </details>

    <UiAlert v-if="notice" color="success" role="status">{{ notice }}</UiAlert>
    <UiAlert v-if="error" color="danger">{{ error }}<template #action><UiButton variant="ghost" color="danger" size="sm" @click="refreshAll">重试</UiButton></template></UiAlert>
    <div v-if="loading" class="loading-panel"><UiIndicator label="正在读取 API Keys" />正在读取 API Keys...</div>
    <UiEmptyState v-else-if="source?.status === 'unavailable' || !source" title="API Keys 暂不可用"><template #action><UiButton variant="outline" color="secondary" size="sm" @click="refreshAll">重试</UiButton></template></UiEmptyState>
    <UiEmptyState v-else-if="source.status === 'empty'" title="暂无数据" description="创建 Key 后即可设置分组、额度与访问限制。" />
    <template v-else>
    <div class="keys-table-wrap">
      <table class="keys-table">
        <thead><tr><th>名称</th><th v-if="visible.group">分组 / 快捷换组</th><th v-if="visible.status">状态 / 当前并发</th><th v-if="visible.quota">配额 / 用量</th><th v-if="visible.rate">5h / 1d / 7d 消费限额</th><th v-if="visible.expires">过期时间</th><th v-if="visible.lastUsed">最近使用</th><th v-if="visible.created">创建时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="key in keys" :key="key.id">
            <tr>
              <td><strong>{{ key.name }}</strong><small>#{{ key.id }} · {{ key.kind === "workspace" ? "系统 Key" : "普通 Key" }}</small></td>
              <td v-if="visible.group" class="key-group-cell"><UiSelect v-if="canManage(key)" :model-value="key.groupId || ''" aria-label="快捷换组" :disabled="busy" @update:model-value="changeGroup(key, $event)"><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></UiSelect><span v-else>{{ groupName(key.groupId) }}</span><small v-if="groupMeta(key.groupId)" class="key-group-meta">{{ groupMeta(key.groupId) }}</small></td>
              <td v-if="visible.status"><UiBadge pill dot :color="key.status === 'active' ? 'success' : key.status === 'quota_exhausted' ? 'warning' : 'secondary'">{{ statusLabel(key.status) }}</UiBadge><small>{{ key.currentConcurrency }} 并发</small></td>
              <td v-if="visible.quota"><div class="key-quota-meter"><span>{{ key.quotaUsdMicros ? formatUsdMicros(key.quotaUsdMicros) : "不限" }}</span><small>已用 {{ formatUsdMicros(key.quotaUsedUsdMicros) }}</small><progress v-if="keyQuotaProgress(key) !== null" class="key-quota-progress" :value="keyQuotaProgress(key) ?? 0" max="100" :aria-label="`${key.name} 配额使用进度`" /></div></td>
              <td v-if="visible.rate"><span>{{ formatUsdMicros(key.rateLimit5hUsdMicros) }} / {{ formatUsdMicros(key.rateLimit1dUsdMicros) }} / {{ formatUsdMicros(key.rateLimit7dUsdMicros) }}</span><small>已用 {{ formatUsdMicros(key.usage5hUsdMicros) }} / {{ formatUsdMicros(key.usage1dUsdMicros) }} / {{ formatUsdMicros(key.usage7dUsdMicros) }}</small></td>
              <td v-if="visible.expires">{{ key.expiresAt ? formatDate(key.expiresAt, true) : "永不过期" }}</td>
              <td v-if="visible.lastUsed"><span>{{ key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "尚未使用" }}</span><small v-if="key.lastUsedIp">{{ key.lastUsedIp }}</small></td>
              <td v-if="visible.created">{{ key.createdAt ? formatDate(key.createdAt, true) : "-" }}</td>
              <td><div class="desktop-key-actions">
                <UiTooltip :text="revealed?.id === key.id ? '隐藏 Key' : '显示 Key'"><UiButton variant="ghost" color="secondary" size="sm" :aria-label="revealed?.id === key.id ? '隐藏 Key' : '显示 Key'" :disabled="busy" @click="reveal(key)"><EyeOff v-if="revealed?.id === key.id" :size="16" /><Eye v-else :size="16" /></UiButton></UiTooltip>
                <UiTooltip text="使用说明"><UiButton variant="ghost" color="secondary" size="sm" aria-label="使用说明" :disabled="busy" @click="openUse(key)"><BookOpen :size="16" /></UiButton></UiTooltip>
                <UiMenu :items="keySecondaryMenuItems(key)" :label="`${key.name} 更多操作`" @select="handleKeyMenu(key, $event)"><template #trigger="{ open, toggle }"><UiButton variant="ghost" color="secondary" size="sm" aria-haspopup="menu" :aria-expanded="open" :aria-label="`${key.name} 更多操作`" @click="toggle"><MoreHorizontal :size="17" /></UiButton></template></UiMenu>
              </div><div class="mobile-key-actions"><UiMenu :items="keyMenuItems(key)" :label="`${key.name} 操作`" @select="handleKeyMenu(key, $event)"><template #trigger="{ open, toggle }"><UiButton variant="ghost" color="secondary" size="sm" aria-haspopup="menu" :aria-expanded="open" :aria-label="`${key.name} 操作`" @click="toggle"><MoreHorizontal :size="17" /></UiButton></template></UiMenu></div></td>
            </tr>
            <tr v-if="revealed?.id === key.id" class="secret-row"><td :colspan="columnCount"><div><code>{{ revealed.value }}</code><UiCopyButton :value="revealed.value" @copied="notice = 'Key 已复制'" /></div></td></tr>
          </template>
        </tbody>
      </table>
    </div>
    <div class="mobile-key-list" aria-label="API Key 列表">
      <article v-for="key in keys" :key="key.id" class="mobile-key-card">
        <header>
          <div class="mobile-key-card__identity">
            <strong>{{ key.name }}</strong>
            <small>#{{ key.id }} · {{ key.kind === "workspace" ? "系统 Key" : "普通 Key" }}</small>
          </div>
          <div class="mobile-key-card__actions">
            <UiBadge pill dot :color="key.status === 'active' ? 'success' : key.status === 'quota_exhausted' ? 'warning' : 'secondary'">{{ statusLabel(key.status) }}</UiBadge>
            <UiMenu :items="keyMenuItems(key)" :label="`${key.name} 操作`" @select="handleKeyMenu(key, $event)">
              <template #trigger="{ open, toggle }"><UiButton variant="ghost" color="secondary" size="sm" aria-haspopup="menu" :aria-expanded="open" :aria-label="`${key.name} 操作`" @click="toggle"><MoreHorizontal :size="17" /></UiButton></template>
            </UiMenu>
          </div>
        </header>
        <dl>
          <div class="mobile-key-card__detail--wide mobile-key-card__group">
            <dt>分组</dt>
            <dd><UiSelect v-if="canManage(key)" :model-value="key.groupId || ''" aria-label="快捷换组" :disabled="busy" @update:model-value="changeGroup(key, $event)"><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></UiSelect><span v-else>{{ groupName(key.groupId) }}</span><small v-if="groupMeta(key.groupId)" class="key-group-meta">{{ groupMeta(key.groupId) }}</small></dd>
          </div>
          <div>
            <dt>配额</dt>
            <dd><strong>{{ key.quotaUsdMicros ? formatUsdMicros(key.quotaUsdMicros) : "不限" }}</strong><small>已用 {{ formatUsdMicros(key.quotaUsedUsdMicros) }}</small><progress v-if="keyQuotaProgress(key) !== null" class="key-quota-progress" :value="keyQuotaProgress(key) ?? 0" max="100" :aria-label="`${key.name} 配额使用进度`" /></dd>
          </div>
          <div>
            <dt>最近使用</dt>
            <dd><strong>{{ key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "尚未使用" }}</strong><small v-if="key.lastUsedIp">{{ key.lastUsedIp }}</small></dd>
          </div>
          <div class="mobile-key-card__detail--wide">
            <dt>5h / 1d / 7d 消费限额</dt>
            <dd><strong>{{ formatUsdMicros(key.rateLimit5hUsdMicros) }} / {{ formatUsdMicros(key.rateLimit1dUsdMicros) }} / {{ formatUsdMicros(key.rateLimit7dUsdMicros) }}</strong><small>已用 {{ formatUsdMicros(key.usage5hUsdMicros) }} / {{ formatUsdMicros(key.usage1dUsdMicros) }} / {{ formatUsdMicros(key.usage7dUsdMicros) }}</small></dd>
          </div>
        </dl>
        <div v-if="revealed?.id === key.id" class="mobile-key-secret"><code>{{ revealed.value }}</code><UiCopyButton :value="revealed.value" @copied="notice = 'Key 已复制'" /></div>
      </article>
    </div>
    </template>

    <footer class="key-pagination"><span>共 {{ total }} 条</span><UiButton variant="outline" color="secondary" size="sm" aria-label="上一页" :disabled="query.page <= 1 || loading" @click="changePage(query.page - 1)"><ChevronLeft :size="16" /></UiButton><span>{{ query.page }} / {{ pages || 1 }}</span><UiButton variant="outline" color="secondary" size="sm" aria-label="下一页" :disabled="query.page >= pages || loading" @click="changePage(query.page + 1)"><ChevronRight :size="16" /></UiButton></footer>
  </section>

  <div v-if="dialog" class="keys-modal-backdrop" @click.self="closeDialog">
      <section ref="dialogRoot" class="keys-modal" role="dialog" aria-modal="true" aria-labelledby="keys-dialog-title" @keydown="onDialogKeydown">
        <header><h3 id="keys-dialog-title">{{ dialog === "key" ? (editingKey ? "编辑 API Key" : "创建 API Key") : dialog === "delete" ? "删除 API Key" : "使用说明" }}</h3><UiButton variant="ghost" color="secondary" size="sm" aria-label="关闭" @click="closeDialog"><X :size="18" /></UiButton></header>
      <form v-if="dialog === 'key'" @submit.prevent="submitKey">
        <div class="form-grid"><UiInput v-model="form.name" label="名称" data-autofocus required maxlength="100" /><UiSelect v-model="form.groupId" label="分组" required><option disabled value="">请选择分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ groupOptionLabel(group) }}</option></UiSelect><UiInput v-model="form.quotaUsd" label="配额（USD，0 为不限）" type="number" min="0" step="0.000001" required /><UiInput v-if="!editingKey" v-model="form.expiresInDays" label="有效天数" type="number" min="1" max="3650" step="1" /><UiInput v-else v-model="form.expiresAt" label="过期时间" type="datetime-local" /></div>
        <details class="key-advanced-settings"><summary>高级限制</summary><div class="key-advanced-settings__body"><div class="form-grid"><UiInput v-model="form.rateLimit5hUsd" label="5 小时消费限额（USD）" type="number" min="0" step="0.000001" /><UiInput v-model="form.rateLimit1dUsd" label="1 天消费限额（USD）" type="number" min="0" step="0.000001" /><UiInput v-model="form.rateLimit7dUsd" label="7 天消费限额（USD）" type="number" min="0" step="0.000001" /></div><div class="form-grid"><UiTextarea v-model="form.ipWhitelist" label="IP 白名单" :rows="3" placeholder="每行一个 IP 或 CIDR" /><UiTextarea v-model="form.ipBlacklist" label="IP 黑名单" :rows="3" placeholder="每行一个 IP 或 CIDR" /></div></div></details>
        <footer><UiButton variant="outline" color="secondary" @click="closeDialog">取消</UiButton><UiButton type="submit" :loading="busy" :disabled="!form.groupId">{{ editingKey ? "保存" : "创建" }}</UiButton></footer>
      </form>
      <div v-else-if="dialog === 'delete'" class="confirm-body"><UiAlert color="danger" title="删除后无法恢复">确认删除 <strong>{{ pendingDelete?.name }}</strong>？</UiAlert><footer><UiButton variant="outline" color="secondary" @click="closeDialog">取消</UiButton><UiButton color="danger" :loading="busy" @click="removeKey">删除</UiButton></footer></div>
      <div v-else class="use-body"><dl><div><dt>API Endpoint</dt><dd><code>{{ endpoint }}</code></dd></div><div><dt>分组平台</dt><dd><code>{{ groupPlatform }}</code></dd></div></dl><UiCodeBlock :code="useConfiguration" language="json" copy-label="复制配置" @copied="notice = '配置已复制'" /><footer><UiButton variant="outline" color="secondary" @click="closeDialog">关闭</UiButton></footer></div>
    </section>
  </div>
</template>

<style scoped>
.keys-panel { min-width: 0; }
.key-filter-disclosure > summary { display: none; }
.key-filter-disclosure > .key-filter-fields { display: grid; }
.keys-header, .header-actions, .endpoint-line, .key-pagination, .keys-modal header, .keys-modal footer { display: flex; align-items: center; }
.keys-header { justify-content: space-between; gap: 16px; margin-bottom: 18px; padding: 18px 20px 0; }
.keys-header__copy { display: grid; min-width: 0; gap: 8px; }
.keys-header h2 { margin: 0; font-size: 18px; }
.header-actions, .endpoint-line, .key-pagination, .keys-modal footer { gap: 8px; }
.endpoint-line { flex-wrap: wrap; color: var(--muted, #57606a); font-size: 13px; }
.endpoint-line code { min-width: 0; color: var(--text, #24292f); overflow-wrap: anywhere; }
.muted { color: #6e7781; }
.key-filters { display: grid; grid-template-columns: minmax(180px, 1.4fr) repeat(5, minmax(110px, .7fr)) auto auto; gap: 10px; align-items: end; margin-bottom: 0; padding: 0 20px 18px; }
.key-filters > * { min-width: 0; }
.column-control { align-self: end; }
.column-options { display: grid; min-width: 150px; gap: 10px; padding: 4px; }
.keys-panel > .ui-alert { margin: 0 20px 12px; }
.keys-table-wrap { width: 100%; overflow: auto; }
.keys-table { width: 100%; min-width: 1080px; border-collapse: collapse; }
.keys-table th, .keys-table td { padding: 11px 10px; border-bottom: 1px solid #d0d7de; text-align: left; vertical-align: top; font-size: 13px; }
.keys-table th { color: #57606a; font-size: 12px; white-space: nowrap; }
.keys-table td > strong, .keys-table td > span, .keys-table td > small { display: block; }
.keys-table td small { margin-top: 4px; color: #57606a; }
.key-group-meta { line-height: 1.35; }
.key-quota-meter { display: grid; min-width: 8rem; gap: 4px; }
.key-quota-progress { width: 100%; height: 6px; border: 0; border-radius: 999px; overflow: hidden; background: var(--color-surface-tertiary); accent-color: var(--color-background-primary-solid); }
.key-quota-progress::-webkit-progress-bar { border-radius: 999px; background: var(--color-surface-tertiary); }
.key-quota-progress::-webkit-progress-value { border-radius: 999px; background: var(--color-background-primary-solid); }
.key-quota-progress::-moz-progress-bar { border-radius: 999px; background: var(--color-background-primary-solid); }
.keys-table :deep(.ui-field) { min-width: 9rem; }
.mobile-key-list { display: none; }
.desktop-key-actions { display: flex; min-width: 7rem; align-items: center; gap: 2px; }
.mobile-key-actions { display: none; }
.secret-row td { background: #f6f8fa; }
.secret-row div { display: flex; align-items: center; gap: 12px; }
.secret-row code { overflow-wrap: anywhere; }
.key-pagination { justify-content: flex-end; margin-top: 14px; padding: 0 20px 14px; color: #57606a; font-size: 13px; }
.key-pagination > span:first-child { margin-right: auto; }
.keys-modal-backdrop { position: fixed; z-index: 100; inset: 0; display: grid; place-items: center; padding: 20px; background: rgb(31 35 40 / 50%); }
.keys-modal { width: min(720px, 100%); max-height: calc(100vh - 40px); overflow: auto; border: 1px solid var(--color-border); border-radius: var(--radius-md); background: var(--color-surface-raised); padding: 20px; box-shadow: var(--elevation-300); }
.keys-modal header { justify-content: space-between; margin-bottom: 18px; }
.keys-modal h3 { margin: 0; font-size: 17px; }
.keys-modal form { display: grid; gap: 14px; }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.key-advanced-settings { border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-surface-secondary); }
.key-advanced-settings summary { min-height: 44px; cursor: pointer; padding: 12px 14px; color: var(--color-text); font-size: var(--font-text-sm); font-weight: var(--font-weight-semibold); }
.key-advanced-settings__body { display: grid; gap: 12px; border-top: 1px solid var(--color-border); padding: 14px; }
.keys-modal footer { justify-content: flex-end; margin-top: 8px; }
.confirm-body, .use-body { display: grid; gap: 16px; }
.use-body dl { display: grid; gap: 12px; margin: 0; }
.use-body dl > div { display: grid; grid-template-columns: 120px 1fr; gap: 12px; }
.use-body dt { color: #57606a; }
.use-body dd { margin: 0; overflow-wrap: anywhere; }
@media (max-width: 1100px) { .key-filters { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 820px) {
  .keys-header { align-items: stretch; flex-direction: column; padding: 16px 16px 0; }
  .endpoint-line { align-items: flex-start; flex-direction: column; }
  .key-filter-disclosure { border-bottom: 1px solid var(--color-border); }
  .key-filter-disclosure > summary { display: flex; min-height: 44px; align-items: center; justify-content: space-between; padding: 0 16px; color: var(--color-text); font-size: var(--font-text-sm); font-weight: var(--font-weight-semibold); cursor: pointer; list-style: none; }
  .key-filter-disclosure > summary::-webkit-details-marker { display: none; }
  .key-filter-disclosure > summary svg { color: var(--color-text-tertiary); transition: transform var(--motion-fast) ease; }
  .key-filter-disclosure[open] > summary svg { transform: rotate(180deg); }
  .key-filters, .form-grid { grid-template-columns: 1fr; }
  .key-filters { padding: 0 16px 16px; }
  .keys-panel > .ui-alert { margin-inline: 16px; }
  .header-actions { flex-shrink: 0; }
  .keys-table-wrap { display: none; }
  .mobile-key-list { display: grid; margin-inline: -1px; border-top: 1px solid var(--color-border); }
  .mobile-key-card { min-width: 0; border-bottom: 1px solid var(--color-border); padding: 16px; }
  .mobile-key-card > header { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: start; gap: 12px; }
  .mobile-key-card__identity { display: grid; min-width: 0; gap: 4px; }
  .mobile-key-card__identity strong { overflow: hidden; color: var(--color-text); font-size: var(--font-text-sm); text-overflow: ellipsis; white-space: nowrap; }
  .mobile-key-card__identity small { overflow: hidden; color: var(--color-text-tertiary); font-size: var(--font-text-xs); text-overflow: ellipsis; white-space: nowrap; }
  .mobile-key-card__actions { display: flex; align-items: center; gap: 4px; }
  .mobile-key-card dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 18px; margin: 16px 0 0; }
  .mobile-key-card dl > div { min-width: 0; }
  .mobile-key-card__detail--wide { grid-column: 1 / -1; }
  .mobile-key-card dt { margin-bottom: 4px; color: var(--color-text-tertiary); font-size: var(--font-text-xs); }
  .mobile-key-card dd { display: grid; min-width: 0; gap: 3px; margin: 0; color: var(--color-text); font-size: var(--font-text-sm); overflow-wrap: anywhere; }
  .mobile-key-card dd small { color: var(--color-text-tertiary); font-size: var(--font-text-xs); }
  .mobile-key-card :deep(.ui-select__control) { min-height: var(--control-size-sm); }
  .mobile-key-secret { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px; margin-top: 14px; border-radius: var(--radius-sm); padding: 10px; background: var(--color-surface-secondary); }
  .mobile-key-secret code { min-width: 0; overflow-wrap: anywhere; }
  .key-pagination { padding: 0 16px 14px; }
  .desktop-key-actions { display: none; }
  .mobile-key-actions { display: block; }
  .keys-modal { padding: 16px; }
}
</style>
