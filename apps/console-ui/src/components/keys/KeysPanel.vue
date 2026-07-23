<script setup lang="ts">
import {
  BookOpen,
  ChevronLeft,
  ChevronRight,
  Columns3,
  Copy,
  Eye,
  EyeOff,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
  X
} from "@lucide/vue";
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";

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
  GatewayGroupPageDTO,
  GatewayKeyListQuery,
  GatewayKeyPageDTO,
  GatewayKeySecretDTO,
  GatewayKeySummaryDTO,
  SourceEnvelope,
  UpdateGatewayKeyRequest
} from "../../api/dtos.ts";
import { formatDate, formatUsdMicros } from "../../console-model.ts";

const props = defineProps<{ csrfToken: string }>();

type Dialog = "" | "key" | "delete" | "use";
type Column = "group" | "status" | "quota" | "rate" | "expires" | "lastUsed" | "created";
const reservedWorkspaceKeyName = "opl-workspace";

const source = ref<SourceEnvelope<GatewayKeyPageDTO> | null>(null);
const groupsSource = ref<SourceEnvelope<GatewayGroupPageDTO> | null>(null);
const endpointSource = ref<SourceEnvelope<GatewayEndpointDTO> | null>(null);
const loading = ref(false);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const dialog = ref<Dialog>("");
const editingKey = ref<GatewayKeySummaryDTO | null>(null);
const pendingDelete = ref<GatewayKeySummaryDTO | null>(null);
const useKey = ref<GatewayKeySummaryDTO | null>(null);
const revealed = ref<GatewayKeySecretDTO | null>(null);
const columnsOpen = ref(false);
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
  created: false
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

async function copyText(value: string, message: string) {
  await navigator.clipboard.writeText(value);
  notice.value = message;
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
  return groups.value.find((group) => group.id === groupId)?.name || "未分组";
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

function openCreate() {
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

function changeGroup(key: GatewayKeySummaryDTO, event: Event) {
  const groupId = (event.target as HTMLSelectElement).value;
  if (groupId && groupId !== key.groupId) void runKeyMutation(key, { groupId }, "分组已更新");
}

function toggleKey(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { enabled: key.status !== "active" }, key.status === "active" ? "API Key 已停用" : "API Key 已启用");
}

function resetQuota(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { resetQuota: true }, "配额用量已重置");
}

function resetRateLimit(key: GatewayKeySummaryDTO) {
  void runKeyMutation(key, { resetRateLimitUsage: true }, "限速用量已重置");
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

onMounted(() => { if (props.csrfToken) void refreshAll(); });
onBeforeUnmount(() => {
  sessionGeneration += 1;
  clearKeyState();
});
</script>

<template>
  <section class="keys-panel panel">
    <header class="keys-header">
      <div>
        <h2>API Keys</h2>
        <div class="endpoint-line">
          <span>API Endpoint</span>
          <code v-if="endpoint">{{ endpoint }}</code>
          <span v-else class="muted">暂不可用</span>
          <button class="icon-command" type="button" title="复制 endpoint" :disabled="!endpoint" @click="copyText(endpoint, 'Endpoint 已复制')">
            <Copy :size="15" />复制 endpoint
          </button>
        </div>
      </div>
      <div class="header-actions">
        <button class="icon-button" type="button" title="刷新" aria-label="刷新 API Keys" :disabled="loading" @click="refreshAll"><RefreshCw :size="17" /></button>
        <button class="button primary" type="button" :disabled="!groups.length" @click="openCreate"><Plus :size="16" />创建 Key</button>
      </div>
    </header>

    <form class="key-filters" @submit.prevent="loadKeys(true)">
      <label class="search-field"><span>搜索 Key</span><span class="input-with-icon"><Search :size="15" /><input v-model.trim="query.search" maxlength="100" /></span></label>
      <label><span>分组筛选</span><select v-model="query.groupId" @change="loadKeys(true)"><option value="">全部分组</option><option value="0">未分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
      <label><span>状态筛选</span><select v-model="query.status" @change="loadKeys(true)"><option value="">全部状态</option><option value="active">启用</option><option value="disabled">停用</option><option value="quota_exhausted">额度用尽</option><option value="expired">已过期</option></select></label>
      <label><span>排序</span><select v-model="query.sortBy" @change="loadKeys(true)"><option value="createdAt">创建时间</option><option value="name">名称</option><option value="id">ID</option><option value="currentConcurrency">当前并发</option><option value="expiresAt">过期时间</option><option value="status">状态</option><option value="lastUsedAt">最近使用</option></select></label>
      <label><span>顺序</span><select v-model="query.sortOrder" @change="loadKeys(true)"><option value="desc">降序</option><option value="asc">升序</option></select></label>
      <label><span>每页</span><select v-model.number="query.pageSize" @change="loadKeys(true)"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option></select></label>
      <button class="button secondary filter-submit" type="submit">查询</button>
      <div class="column-control">
        <button class="button secondary" type="button" @click="columnsOpen = !columnsOpen"><Columns3 :size="16" />列设置</button>
        <div v-if="columnsOpen" class="column-menu">
          <label v-for="item in [{ key: 'group', label: '分组' }, { key: 'status', label: '状态' }, { key: 'quota', label: '配额' }, { key: 'rate', label: '限速' }, { key: 'expires', label: '过期' }, { key: 'lastUsed', label: '最近使用' }, { key: 'created', label: '创建时间' }]" :key="item.key"><input v-model="visible[item.key as Column]" type="checkbox" />{{ item.label }}</label>
        </div>
      </div>
    </form>

    <p v-if="notice" class="inline-notice">{{ notice }}</p>
    <p v-if="error" class="inline-error">{{ error }} <button class="text-button" type="button" @click="refreshAll">重试</button></p>
    <div v-if="loading" class="loading-panel"><span class="spinner" />正在读取 API Keys...</div>
    <div v-else-if="source?.status === 'unavailable' || !source" class="empty-panel">暂不可用</div>
    <div v-else-if="source.status === 'empty'" class="empty-panel">暂无数据</div>
    <div v-else class="keys-table-wrap">
      <table class="keys-table">
        <thead><tr><th>名称</th><th v-if="visible.group">分组 / 快捷换组</th><th v-if="visible.status">状态</th><th v-if="visible.quota">配额</th><th v-if="visible.rate">5h / 1d / 7d 限速</th><th v-if="visible.expires">过期时间</th><th v-if="visible.lastUsed">最近使用</th><th v-if="visible.created">创建时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="key in keys" :key="key.id">
            <tr>
              <td><strong>{{ key.name }}</strong><small>#{{ key.id }} · {{ key.kind === "workspace" ? "系统 Key" : "普通 Key" }}</small></td>
              <td v-if="visible.group"><select v-if="canManage(key)" :value="key.groupId || ''" aria-label="快捷换组" :disabled="busy" @change="changeGroup(key, $event)"><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select><span v-else>{{ groupName(key.groupId) }}</span></td>
              <td v-if="visible.status"><span class="status-pill" :class="{ good: key.status === 'active' }">{{ statusLabel(key.status) }}</span><small>{{ key.currentConcurrency }} 并发</small></td>
              <td v-if="visible.quota"><span>{{ key.quotaUsdMicros ? formatUsdMicros(key.quotaUsdMicros) : "不限" }}</span><small>已用 {{ formatUsdMicros(key.quotaUsedUsdMicros) }}</small></td>
              <td v-if="visible.rate"><span>{{ formatUsdMicros(key.rateLimit5hUsdMicros) }} / {{ formatUsdMicros(key.rateLimit1dUsdMicros) }} / {{ formatUsdMicros(key.rateLimit7dUsdMicros) }}</span><small>已用 {{ formatUsdMicros(key.usage5hUsdMicros) }} / {{ formatUsdMicros(key.usage1dUsdMicros) }} / {{ formatUsdMicros(key.usage7dUsdMicros) }}</small></td>
              <td v-if="visible.expires">{{ key.expiresAt ? formatDate(key.expiresAt, true) : "永不过期" }}</td>
              <td v-if="visible.lastUsed"><span>{{ key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "尚未使用" }}</span><small v-if="key.lastUsedIp">{{ key.lastUsedIp }}</small></td>
              <td v-if="visible.created">{{ key.createdAt ? formatDate(key.createdAt, true) : "-" }}</td>
              <td><div class="row-actions">
                <button class="icon-command" type="button" :disabled="busy" @click="reveal(key)"><EyeOff v-if="revealed?.id === key.id" :size="15" /><Eye v-else :size="15" />{{ revealed?.id === key.id ? "隐藏" : "揭示" }}</button>
                <button class="icon-command" type="button" title="使用说明" :disabled="busy" @click="openUse(key)"><BookOpen :size="15" />使用说明</button>
                <button v-if="canManage(key)" class="icon-command" type="button" title="编辑" :disabled="busy" @click="openEdit(key)"><Pencil :size="15" />编辑</button>
                <button v-if="canManage(key)" class="icon-command" type="button" :disabled="busy" @click="toggleKey(key)"><Power :size="15" />{{ key.status === "active" ? "停用" : "启用" }}</button>
                <button v-if="canManage(key)" class="icon-command" type="button" title="重置配额" :disabled="busy" @click="resetQuota(key)"><RotateCcw :size="15" />重置配额</button>
                <button v-if="canManage(key)" class="icon-command" type="button" title="重置限速" :disabled="busy" @click="resetRateLimit(key)"><RotateCcw :size="15" />重置限速</button>
                <button v-if="canDelete(key)" class="icon-command danger" type="button" title="删除" :disabled="busy" @click="askDelete(key)"><Trash2 :size="15" />删除</button>
              </div></td>
            </tr>
            <tr v-if="revealed?.id === key.id" class="secret-row"><td :colspan="columnCount"><div><code>{{ revealed.value }}</code><button class="icon-command" type="button" @click="copyText(revealed.value, 'Key 已复制')"><Copy :size="15" />复制</button></div></td></tr>
          </template>
        </tbody>
      </table>
    </div>

    <footer class="key-pagination"><span>共 {{ total }} 条</span><button class="icon-button" type="button" aria-label="上一页" :disabled="query.page <= 1 || loading" @click="changePage(query.page - 1)"><ChevronLeft :size="16" /></button><span>{{ query.page }} / {{ pages || 1 }}</span><button class="icon-button" type="button" aria-label="下一页" :disabled="query.page >= pages || loading" @click="changePage(query.page + 1)"><ChevronRight :size="16" /></button></footer>
  </section>

  <div v-if="dialog" class="keys-modal-backdrop" @click.self="closeDialog">
      <section class="keys-modal" role="dialog" aria-modal="true" aria-labelledby="keys-dialog-title">
        <header><h3 id="keys-dialog-title">{{ dialog === "key" ? (editingKey ? "编辑 API Key" : "创建 API Key") : dialog === "delete" ? "删除 API Key" : "使用说明" }}</h3><button class="icon-button" type="button" aria-label="关闭" @click="closeDialog"><X :size="18" /></button></header>
      <form v-if="dialog === 'key'" @submit.prevent="submitKey">
        <div class="form-grid"><label>名称<input v-model.trim="form.name" required maxlength="100" /></label><label>分组<select v-model="form.groupId" required><option disabled value="">请选择分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label><label>配额（USD，0 为不限）<input v-model.number="form.quotaUsd" type="number" min="0" step="0.000001" required /></label><label v-if="!editingKey">有效天数<input v-model.number="form.expiresInDays" type="number" min="1" max="3650" step="1" /></label><label v-else>过期时间<input v-model="form.expiresAt" type="datetime-local" /></label><label>5 小时限速（USD）<input v-model.number="form.rateLimit5hUsd" type="number" min="0" step="0.000001" /></label><label>1 天限速（USD）<input v-model.number="form.rateLimit1dUsd" type="number" min="0" step="0.000001" /></label><label>7 天限速（USD）<input v-model.number="form.rateLimit7dUsd" type="number" min="0" step="0.000001" /></label></div>
        <label>IP 白名单<textarea v-model="form.ipWhitelist" rows="3" placeholder="每行一个 IP 或 CIDR" /></label><label>IP 黑名单<textarea v-model="form.ipBlacklist" rows="3" placeholder="每行一个 IP 或 CIDR" /></label>
        <footer><button class="button secondary" type="button" @click="closeDialog">取消</button><button class="button primary" type="submit" :disabled="busy || !form.groupId">{{ busy ? "处理中..." : editingKey ? "保存" : "创建" }}</button></footer>
      </form>
      <div v-else-if="dialog === 'delete'" class="confirm-body"><p>确认删除 <strong>{{ pendingDelete?.name }}</strong>？</p><footer><button class="button secondary" type="button" @click="closeDialog">取消</button><button class="button danger-button" type="button" :disabled="busy" @click="removeKey">删除</button></footer></div>
      <div v-else class="use-body"><dl><div><dt>API Endpoint</dt><dd><code>{{ endpoint }}</code></dd></div><div><dt>分组平台</dt><dd><code>{{ groupPlatform }}</code></dd></div></dl><pre><code>{{ useConfiguration }}</code></pre><footer><button class="button secondary" type="button" @click="closeDialog">关闭</button><button class="button primary" type="button" :disabled="!useConfiguration" @click="copyText(useConfiguration, '配置已复制')"><Copy :size="15" />复制配置</button></footer></div>
    </section>
  </div>
</template>

<style scoped>
.keys-panel { min-width: 0; }
.keys-header, .keys-header > div, .header-actions, .endpoint-line, .row-actions, .key-pagination, .keys-modal header, .keys-modal footer { display: flex; align-items: center; }
.keys-header { justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.keys-header h2 { margin: 0 0 8px; font-size: 18px; }
.header-actions, .endpoint-line, .row-actions, .key-pagination, .keys-modal footer { gap: 8px; }
.endpoint-line { flex-wrap: wrap; color: var(--muted, #667085); font-size: 13px; }
.endpoint-line code { color: var(--text, #182230); }
.muted { color: #98a2b3; }
.key-filters { display: grid; grid-template-columns: minmax(180px, 1.4fr) repeat(5, minmax(110px, .7fr)) auto auto; gap: 10px; align-items: end; margin-bottom: 16px; }
.key-filters label, .keys-modal label { display: grid; gap: 6px; color: #475467; font-size: 12px; }
.key-filters input, .key-filters select, .keys-modal input, .keys-modal select, .keys-modal textarea, .keys-table select { width: 100%; border: 1px solid #d0d5dd; border-radius: 6px; background: #fff; color: #182230; font: inherit; }
.key-filters input, .key-filters select, .keys-modal input, .keys-modal select, .keys-table select { min-height: 36px; padding: 7px 9px; }
.keys-modal textarea { padding: 9px; resize: vertical; }
.input-with-icon { position: relative; display: block; }
.input-with-icon svg { position: absolute; left: 10px; top: 10px; color: #98a2b3; }
.input-with-icon input { padding-left: 32px; }
.column-control { position: relative; }
.column-menu { position: absolute; z-index: 5; right: 0; top: calc(100% + 4px); width: 150px; padding: 8px; border: 1px solid #d0d5dd; border-radius: 6px; background: #fff; box-shadow: 0 8px 24px rgba(16, 24, 40, .12); }
.column-menu label { display: flex; grid-template-columns: none; align-items: center; gap: 8px; padding: 5px; }
.column-menu input { width: 15px; min-height: 15px; }
.keys-table-wrap { width: 100%; overflow: auto; }
.keys-table { width: 100%; min-width: 1080px; border-collapse: collapse; }
.keys-table th, .keys-table td { padding: 11px 10px; border-bottom: 1px solid #eaecf0; text-align: left; vertical-align: top; font-size: 13px; }
.keys-table th { color: #667085; font-size: 12px; white-space: nowrap; }
.keys-table td > strong, .keys-table td > span, .keys-table td > small { display: block; }
.keys-table td small { margin-top: 4px; color: #667085; }
.row-actions { flex-wrap: wrap; min-width: 250px; }
.icon-command { display: inline-flex; align-items: center; gap: 4px; border: 0; background: transparent; color: #344054; padding: 4px; font: inherit; cursor: pointer; }
.icon-command:hover { color: #155eef; }
.icon-command.danger { color: #b42318; }
.icon-command:disabled { cursor: not-allowed; opacity: .45; }
.secret-row td { background: #f8fafc; }
.secret-row div { display: flex; align-items: center; gap: 12px; }
.secret-row code { overflow-wrap: anywhere; }
.key-pagination { justify-content: flex-end; margin-top: 14px; color: #667085; font-size: 13px; }
.key-pagination > span:first-child { margin-right: auto; }
.keys-modal-backdrop { position: fixed; z-index: 100; inset: 0; display: grid; place-items: center; padding: 20px; background: rgba(16, 24, 40, .5); }
.keys-modal { width: min(720px, 100%); max-height: calc(100vh - 40px); overflow: auto; border-radius: 8px; background: #fff; padding: 20px; box-shadow: 0 24px 48px rgba(16, 24, 40, .2); }
.keys-modal header { justify-content: space-between; margin-bottom: 18px; }
.keys-modal h3 { margin: 0; font-size: 17px; }
.keys-modal form { display: grid; gap: 14px; }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.keys-modal footer { justify-content: flex-end; margin-top: 8px; }
.confirm-body, .use-body { display: grid; gap: 16px; }
.use-body dl { display: grid; gap: 12px; margin: 0; }
.use-body dl > div { display: grid; grid-template-columns: 120px 1fr; gap: 12px; }
.use-body dt { color: #667085; }
.use-body dd { margin: 0; overflow-wrap: anywhere; }
.use-body pre { margin: 0; overflow: auto; padding: 12px; border: 1px solid #d0d5dd; background: #f8fafc; }
.danger-button { background: #b42318; color: #fff; border-color: #b42318; }
@media (max-width: 1100px) { .key-filters { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 700px) {
  .keys-header { align-items: flex-start; }
  .keys-header, .endpoint-line { flex-direction: column; align-items: flex-start; }
  .key-filters, .form-grid { grid-template-columns: 1fr; }
  .header-actions { flex-shrink: 0; }
  .keys-modal { padding: 16px; }
}
</style>
