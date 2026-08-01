import {
  BookOpen,
  ChevronLeft,
  ChevronRight,
  Copy,
  Eye,
  EyeOff,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  RotateCcw,
  Save,
  Search,
  ShieldCheck,
  Trash2
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

import { unavailableSource } from "../../app/use-console-controller.ts";
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
import { SourceState } from "../source/SourceState.tsx";
import { Alert, Badge, Button, Field, Modal, Select, Tooltip } from "../ui/index.ts";

export interface KeysPanelProps {
  csrfToken: string;
}

type Dialog = "" | "key" | "delete" | "use";
type KeyQuery = Required<Omit<GatewayKeyListQuery, "groupId">> & { groupId: string };

interface KeyForm {
  name: string;
  groupId: string;
  quotaUsd: string;
  ipWhitelist: string;
  ipBlacklist: string;
  expiresInDays: string;
  expiresAt: string;
  rateLimit5hUsd: string;
  rateLimit1dUsd: string;
  rateLimit7dUsd: string;
}

const secretLifetimeMs = 60_000;
const initialQuery: KeyQuery = {
  page: 1,
  pageSize: 20,
  search: "",
  status: "",
  groupId: "",
  sortBy: "createdAt",
  sortOrder: "desc"
};
const emptyForm: KeyForm = {
  name: "",
  groupId: "",
  quotaUsd: "0",
  ipWhitelist: "",
  ipBlacklist: "",
  expiresInDays: "30",
  expiresAt: "",
  rateLimit5hUsd: "0",
  rateLimit1dUsd: "0",
  rateLimit7dUsd: "0"
};

function friendlyError(value: unknown) {
  const message = value instanceof Error ? value.message : String(value || "");
  if (/upstream_unavailable|failed to fetch|networkerror|gateway_key_unavailable/i.test(message)) {
    return "服务暂不可用，请稍后重试";
  }
  if (/gateway_key_readback_unavailable/i.test(message)) return "无法确认权威回读，请刷新后再操作";
  if (/gateway_key_reserved/i.test(message)) return "Workspace 系统 Key 不允许修改或删除";
  if (/金额格式无效|表单信息无效/.test(message)) return message;
  return message && !message.includes("_") ? message : "请求失败，请稍后重试";
}

function apiErrorCode(value: unknown) {
  const payload = value && typeof value === "object" && "payload" in value
    ? (value as { payload?: unknown }).payload
    : null;
  return payload && typeof payload === "object" ? String((payload as { error?: unknown }).error || "") : "";
}

function parseLines(value: string) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function usdMicros(value: string) {
  const amount = Number(value);
  const micros = Math.round(amount * 1_000_000);
  if (!Number.isFinite(amount) || amount < 0 || !Number.isSafeInteger(micros)) throw new Error("金额格式无效");
  return micros;
}

function idempotencyKey(prefix: string) {
  return `${prefix}:${crypto.randomUUID()}`;
}

function sameStrings(left: string[], right: string[]) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function isProtectedWorkspaceKey(key: GatewayKeySummaryDTO) {
  return key.kind === "workspace";
}

function canManage(key: GatewayKeySummaryDTO) {
  return key.manageable && !isProtectedWorkspaceKey(key);
}

function canDelete(key: GatewayKeySummaryDTO) {
  return key.deletable && !isProtectedWorkspaceKey(key);
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

function statusLabel(status: GatewayKeySummaryDTO["status"]) {
  return { active: "启用", disabled: "停用", quota_exhausted: "额度用尽", expired: "已过期" }[status];
}

function statusColor(status: GatewayKeySummaryDTO["status"]): "success" | "secondary" | "warning" | "danger" {
  if (status === "active") return "success";
  if (status === "quota_exhausted") return "warning";
  if (status === "expired") return "danger";
  return "secondary";
}

function groupMetadataLabel(group: GatewayGroupDTO) {
  const status = group.status === "active" ? "可用" : group.status === "disabled" ? "停用" : group.status;
  return `${group.platform} · ${group.rateMultiplier}x · ${status}`;
}

export function KeysPanel({ csrfToken }: KeysPanelProps) {
  const [source, setSource] = useState<SourceEnvelope<GatewayKeyPageDTO> | null>(null);
  const [groupsSource, setGroupsSource] = useState<SourceEnvelope<GatewayGroupPageDTO> | null>(null);
  const [endpointSource, setEndpointSource] = useState<SourceEnvelope<GatewayEndpointDTO> | null>(null);
  const [query, setQuery] = useState<KeyQuery>(initialQuery);
  const [loading, setLoading] = useState(false);
  const [referenceLoading, setReferenceLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [referenceError, setReferenceError] = useState("");
  const [notice, setNotice] = useState("");
  const [dialog, setDialog] = useState<Dialog>("");
  const [editingKey, setEditingKey] = useState<GatewayKeySummaryDTO | null>(null);
  const [pendingDelete, setPendingDelete] = useState<GatewayKeySummaryDTO | null>(null);
  const [useKey, setUseKey] = useState<GatewayKeySummaryDTO | null>(null);
  const [revealed, setRevealed] = useState<GatewayKeySecretDTO | null>(null);
  const [form, setForm] = useState<KeyForm>(emptyForm);
  const sessionGeneration = useRef(0);
  const listGeneration = useRef(0);
  const csrfRef = useRef(csrfToken);
  const secretTimer = useRef<number | undefined>(undefined);
  const createIntent = useRef<{ signature: string; input: CreateGatewayKeyRequest; key: string } | null>(null);
  const updateIntents = useRef(new Map<string, { signature: string; key: string }>());
  const deleteIntents = useRef(new Map<string, string>());
  csrfRef.current = csrfToken;

  const groups = useMemo(() => groupsSource?.available ? groupsSource.data.items : [], [groupsSource]);
  const endpoint = endpointSource?.available ? endpointSource.data.baseUrl : "";
  const pages = source?.available ? source.data.pages : 0;

  const clearSecret = useCallback(() => {
    setRevealed(null);
    if (secretTimer.current !== undefined) window.clearTimeout(secretTimer.current);
    secretTimer.current = undefined;
  }, []);

  const armSecretTimer = useCallback(() => {
    if (secretTimer.current !== undefined) window.clearTimeout(secretTimer.current);
    secretTimer.current = window.setTimeout(clearSecret, secretLifetimeMs);
  }, [clearSecret]);

  const requestIsCurrent = useCallback((generation: number, token: string) => {
    return generation === sessionGeneration.current && token === csrfRef.current;
  }, []);

  const loadKeys = useCallback(async (nextQuery: KeyQuery) => {
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    const request = ++listGeneration.current;
    setQuery(nextQuery);
    setLoading(true);
    setError("");
    clearSecret();
    try {
      const result = await getGatewayKeys(nextQuery);
      if (request !== listGeneration.current || !requestIsCurrent(session, token)) return;
      if (result.available && result.data.page !== nextQuery.page) throw new Error("gateway_key_page_mismatch");
      setSource(result);
    } catch (value) {
      if (request !== listGeneration.current || !requestIsCurrent(session, token)) return;
      setSource(unavailableSource("sub2api"));
      setError("");
    } finally {
      if (request === listGeneration.current && requestIsCurrent(session, token)) setLoading(false);
    }
  }, [clearSecret, requestIsCurrent]);

  const loadReferenceData = useCallback(async () => {
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    setReferenceLoading(true);
    setReferenceError("");
    const [groupResult, endpointResult] = await Promise.allSettled([getGatewayGroups(), getGatewayEndpoint()]);
    if (!requestIsCurrent(session, token)) return;
    setGroupsSource(groupResult.status === "fulfilled" ? groupResult.value : unavailableSource("sub2api"));
    setEndpointSource(endpointResult.status === "fulfilled" ? endpointResult.value : unavailableSource("sub2api"));
    if (groupResult.status === "rejected" || endpointResult.status === "rejected") {
      setReferenceError("部分 API 配置暂不可用，可刷新后重试");
    }
    setReferenceLoading(false);
  }, [requestIsCurrent]);

  const refreshAll = useCallback(async () => {
    await Promise.all([loadReferenceData(), loadKeys(query)]);
  }, [loadKeys, loadReferenceData, query]);

  useEffect(() => {
    sessionGeneration.current += 1;
    listGeneration.current += 1;
    setSource(null);
    setGroupsSource(null);
    setEndpointSource(null);
    setQuery(initialQuery);
    setLoading(false);
    setReferenceLoading(false);
    setBusy(false);
    setError("");
    setReferenceError("");
    setNotice("");
    setDialog("");
    setEditingKey(null);
    setPendingDelete(null);
    setUseKey(null);
    createIntent.current = null;
    updateIntents.current.clear();
    deleteIntents.current.clear();
    clearSecret();
    if (csrfToken) {
      void loadReferenceData();
      void loadKeys(initialQuery);
    }
    return () => {
      sessionGeneration.current += 1;
      listGeneration.current += 1;
      clearSecret();
    };
  }, [clearSecret, csrfToken, loadKeys, loadReferenceData]);

  const groupName = (groupId: string | null) => groups.find((group) => group.id === groupId)?.name || "未分组";
  const groupMeta = (groupId: string | null) => {
    const group = groups.find((item) => item.id === groupId);
    return group ? groupMetadataLabel(group) : "";
  };

  const copyText = async (value: string, success: string) => {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      setNotice(success);
    } catch {
      setError("复制失败，请手动选择内容");
    }
  };

  const updateForm = (field: keyof KeyForm, value: string) => setForm((current) => ({ ...current, [field]: value }));

  const openCreate = () => {
    setEditingKey(null);
    setForm({ ...emptyForm, groupId: groups[0]?.id || "" });
    setDialog("key");
  };

  const openEdit = (key: GatewayKeySummaryDTO) => {
    if (!canManage(key)) return;
    setEditingKey(key);
    setForm({
      name: key.name,
      groupId: key.groupId || "",
      quotaUsd: String(key.quotaUsdMicros / 1_000_000),
      ipWhitelist: key.ipWhitelist.join("\n"),
      ipBlacklist: key.ipBlacklist.join("\n"),
      expiresInDays: "30",
      expiresAt: key.expiresAt ? key.expiresAt.slice(0, 16) : "",
      rateLimit5hUsd: String(key.rateLimit5hUsdMicros / 1_000_000),
      rateLimit1dUsd: String(key.rateLimit1dUsdMicros / 1_000_000),
      rateLimit7dUsd: String(key.rateLimit7dUsdMicros / 1_000_000)
    });
    setDialog("key");
  };

  const closeDialog = () => {
    if (busy) return;
    setDialog("");
    setEditingKey(null);
    setPendingDelete(null);
    setUseKey(null);
  };

  const createRequest = (): CreateGatewayKeyRequest => ({
    name: form.name.trim(),
    groupId: form.groupId,
    ipWhitelist: parseLines(form.ipWhitelist),
    ipBlacklist: parseLines(form.ipBlacklist),
    quotaUsdMicros: usdMicros(form.quotaUsd),
    expiresInDays: Number(form.expiresInDays) > 0 ? Number(form.expiresInDays) : undefined,
    rateLimit5hUsdMicros: usdMicros(form.rateLimit5hUsd),
    rateLimit1dUsdMicros: usdMicros(form.rateLimit1dUsd),
    rateLimit7dUsdMicros: usdMicros(form.rateLimit7dUsd)
  });

  const updateRequest = (): UpdateGatewayKeyRequest => ({
    name: form.name.trim(),
    groupId: form.groupId,
    ipWhitelist: parseLines(form.ipWhitelist),
    ipBlacklist: parseLines(form.ipBlacklist),
    quotaUsdMicros: usdMicros(form.quotaUsd),
    expiresAt: form.expiresAt ? new Date(form.expiresAt).toISOString() : "",
    rateLimit5hUsdMicros: usdMicros(form.rateLimit5hUsd),
    rateLimit1dUsdMicros: usdMicros(form.rateLimit1dUsd),
    rateLimit7dUsdMicros: usdMicros(form.rateLimit7dUsd)
  });

  const reveal = async (key: GatewayKeySummaryDTO) => {
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    if (!token || busy) return null;
    if (revealed?.id === key.id) {
      clearSecret();
      return null;
    }
    setBusy(true);
    setError("");
    clearSecret();
    try {
      const result = await revealGatewayKey(key.id, token);
      if (!requestIsCurrent(session, token)) return null;
      if (!result.available || result.data.id !== key.id || !result.data.value) throw new Error("gateway_key_unavailable");
      setRevealed(result.data);
      armSecretTimer();
      return result.data;
    } catch (value) {
      if (requestIsCurrent(session, token)) setError(friendlyError(value));
      return null;
    } finally {
      if (requestIsCurrent(session, token)) setBusy(false);
    }
  };

  const mutateKey = async (key: GatewayKeySummaryDTO, input: UpdateGatewayKeyRequest, message: string) => {
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    if (!token || !canManage(key)) return false;
    const signature = JSON.stringify(input);
    let intent = updateIntents.current.get(key.id);
    if (!intent || intent.signature !== signature) {
      intent = { signature, key: idempotencyKey("key-update") };
      updateIntents.current.set(key.id, intent);
    }
    const updated = await updateGatewayKey(key.id, input, token, intent.key);
    if (!requestIsCurrent(session, token)) return false;
    if (!updated.available || updated.data.id !== key.id || !keyMatchesUpdate(updated.data, input)) throw new Error("gateway_key_unavailable");
    const readback = await getGatewayKey(key.id);
    if (!requestIsCurrent(session, token)) return false;
    if (!readback.available || readback.data.id !== key.id || !keyMatchesUpdate(readback.data, input)) throw new Error("gateway_key_readback_unavailable");
    updateIntents.current.delete(key.id);
    setNotice(message);
    setDialog("");
    await loadKeys(query);
    return true;
  };

  const runKeyMutation = async (key: GatewayKeySummaryDTO, input: UpdateGatewayKeyRequest, message: string) => {
    if (busy || !canManage(key)) return;
    setBusy(true);
    setError("");
    try {
      await mutateKey(key, input, message);
    } catch (value) {
      setError(friendlyError(value));
    } finally {
      setBusy(false);
    }
  };

  const submitKey = async (event: FormEvent) => {
    event.preventDefault();
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    if (busy || !token || !form.name.trim() || !form.groupId) return;
    setBusy(true);
    setError("");
    try {
      if (editingKey) {
        await mutateKey(editingKey, updateRequest(), "API Key 已更新");
        return;
      }
      const input = createRequest();
      if (!input.name || !input.groupId || !input.expiresInDays) throw new Error("表单信息无效");
      const signature = JSON.stringify(input);
      if (!createIntent.current || createIntent.current.signature !== signature) {
        createIntent.current = { signature, input, key: idempotencyKey("key-create") };
      }
      const created = await createGatewayKey(createIntent.current.input, token, createIntent.current.key);
      if (!requestIsCurrent(session, token)) return;
      if (!created.available) throw new Error("gateway_key_unavailable");
      const readback = await getGatewayKey(created.data.id);
      if (!requestIsCurrent(session, token)) return;
      if (!readback.available || readback.data.id !== created.data.id || !keyMatchesCreate(readback.data, input)) {
        throw new Error("gateway_key_readback_unavailable");
      }
      createIntent.current = null;
      setDialog("");
      await loadKeys({ ...query, page: 1 });
      if (!requestIsCurrent(session, token)) return;
      const secret = await revealGatewayKey(created.data.id, token);
      if (!requestIsCurrent(session, token)) return;
      if (!secret.available || secret.data.id !== created.data.id || !secret.data.value) throw new Error("gateway_key_unavailable");
      setRevealed(secret.data);
      armSecretTimer();
      setNotice("API Key 已创建，请立即复制；完整 Key 将在 60 秒后隐藏");
    } catch (value) {
      if (requestIsCurrent(session, token)) setError(friendlyError(value));
    } finally {
      if (requestIsCurrent(session, token)) setBusy(false);
    }
  };

  const removeKey = async () => {
    const key = pendingDelete;
    const token = csrfRef.current;
    const session = sessionGeneration.current;
    if (!key || busy || !token || !canDelete(key)) return;
    setBusy(true);
    setError("");
    const intent = deleteIntents.current.get(key.id) || idempotencyKey("key-delete");
    deleteIntents.current.set(key.id, intent);
    try {
      let commandError: unknown = null;
      try {
        const result = await deleteGatewayKey(key.id, token, intent);
        if (!requestIsCurrent(session, token)) return;
        if (!result.available || result.data.status !== "deleted") commandError = new Error("gateway_key_delete_unavailable");
      } catch (value) {
        commandError = value;
      }

      let confirmedMissing = false;
      try {
        const readback = await getGatewayKey(key.id);
        if (!requestIsCurrent(session, token)) return;
        if (!readback.available) throw new Error("gateway_key_readback_unavailable");
      } catch (value) {
        if (!requestIsCurrent(session, token)) return;
        confirmedMissing = apiErrorCode(value) === "gateway_key_not_found";
        if (!confirmedMissing && !commandError) commandError = value;
      }
      if (!confirmedMissing) throw commandError || new Error("gateway_key_readback_unavailable");
      deleteIntents.current.delete(key.id);
      if (revealed?.id === key.id) clearSecret();
      setNotice("API Key 已删除");
      setDialog("");
      setPendingDelete(null);
      await loadKeys(query);
    } catch (value) {
      if (requestIsCurrent(session, token)) setError(friendlyError(value));
    } finally {
      if (requestIsCurrent(session, token)) setBusy(false);
    }
  };

  const openUse = async (key: GatewayKeySummaryDTO) => {
    setUseKey(key);
    const secret = revealed?.id === key.id ? revealed : await reveal(key);
    if (secret?.id === key.id) setDialog("use");
  };

  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    void loadKeys({ ...query, page: 1 });
  };

  const changeFilter = <K extends keyof KeyQuery>(field: K, value: KeyQuery[K]) => {
    const next = { ...query, [field]: value, page: 1 };
    setQuery(next);
    if (field !== "search") void loadKeys(next);
  };

  const changePage = (page: number) => {
    if (page < 1 || page > pages || page === query.page) return;
    void loadKeys({ ...query, page });
  };

  const useGroup = groups.find((group) => group.id === useKey?.groupId) || null;
  const useConfiguration = useKey && revealed?.id === useKey.id && endpoint && useGroup
    ? JSON.stringify({ platform: useGroup.platform, baseURL: endpoint, apiKey: revealed.value }, null, 2)
    : "";

  const renderKeyActions = (key: GatewayKeySummaryDTO, className = "keys-row-actions") => {
    const manageable = canManage(key);
    return (
      <div className={className}>
        <Tooltip compact content={revealed?.id === key.id ? "隐藏 Key" : "显示 Key"}>
          <Button aria-label={revealed?.id === key.id ? "隐藏 Key" : "显示 Key"} disabled={busy} onClick={() => void reveal(key)} size="sm" uniform variant="ghost">
            {revealed?.id === key.id ? <EyeOff aria-hidden="true" size={16} /> : <Eye aria-hidden="true" size={16} />}
          </Button>
        </Tooltip>
        <Tooltip compact content="使用说明">
          <Button aria-label="使用说明" disabled={busy} onClick={() => void openUse(key)} size="sm" uniform variant="ghost"><BookOpen aria-hidden="true" size={16} /></Button>
        </Tooltip>
        <Tooltip compact content={manageable ? "编辑" : "系统 Key 不可编辑"}>
          <Button aria-label="编辑" disabled={busy || !manageable} onClick={() => openEdit(key)} size="sm" uniform variant="ghost"><Pencil aria-hidden="true" size={16} /></Button>
        </Tooltip>
        <Tooltip compact content={manageable ? (key.status === "active" ? "停用" : "启用") : "系统 Key 不可启停"}>
          <Button aria-label={key.status === "active" ? "停用" : "启用"} disabled={busy || !manageable} onClick={() => void runKeyMutation(key, { enabled: key.status !== "active" }, key.status === "active" ? "API Key 已停用" : "API Key 已启用")} size="sm" uniform variant="ghost"><Power aria-hidden="true" size={16} /></Button>
        </Tooltip>
        <Tooltip compact content={manageable ? "重置配额用量" : "系统 Key 不可重置"}>
          <Button aria-label="重置配额用量" disabled={busy || !manageable} onClick={() => void runKeyMutation(key, { resetQuota: true }, "配额用量已重置")} size="sm" uniform variant="ghost"><RotateCcw aria-hidden="true" size={16} /></Button>
        </Tooltip>
        <Tooltip compact content={manageable ? "重置消费限额用量" : "系统 Key 不可重置"}>
          <Button aria-label="重置消费限额用量" disabled={busy || !manageable} onClick={() => void runKeyMutation(key, { resetRateLimitUsage: true }, "消费限额用量已重置")} size="sm" uniform variant="ghost"><ShieldCheck aria-hidden="true" size={16} /></Button>
        </Tooltip>
        <Tooltip compact content={canDelete(key) ? "删除" : "系统 Key 不可删除"}>
          <Button aria-label="删除" color="danger" disabled={busy || !canDelete(key)} onClick={() => { setPendingDelete(key); setDialog("delete"); }} size="sm" uniform variant="ghost"><Trash2 aria-hidden="true" size={16} /></Button>
        </Tooltip>
      </div>
    );
  };

  const keyForm = (
    <form className="key-form" id="keys-form-submit" onSubmit={submitKey}>
      <div className="key-form-grid">
        <Field autoFocus label="名称" maxLength={100} onChange={(event) => updateForm("name", event.currentTarget.value)} required value={form.name} />
        <Select
          label="分组"
          onChange={(value) => updateForm("groupId", value)}
          options={groups.map((group) => ({ value: group.id, label: group.name, description: groupMetadataLabel(group) }))}
          placeholder="请选择分组"
          value={form.groupId}
        />
        <Field label="配额（USD，0 为不限）" min="0" onChange={(event) => updateForm("quotaUsd", event.currentTarget.value)} required step="0.000001" type="number" value={form.quotaUsd} />
        {editingKey ? (
          <Field label="过期时间" onChange={(event) => updateForm("expiresAt", event.currentTarget.value)} type="datetime-local" value={form.expiresAt} />
        ) : (
          <Field label="有效天数" max="3650" min="1" onChange={(event) => updateForm("expiresInDays", event.currentTarget.value)} required step="1" type="number" value={form.expiresInDays} />
        )}
      </div>
      <details className="key-advanced-settings">
        <summary>高级限制</summary>
        <div className="key-advanced-settings__body">
          <div className="key-form-grid key-form-grid--three">
            <Field label="5 小时消费限额（USD）" min="0" onChange={(event) => updateForm("rateLimit5hUsd", event.currentTarget.value)} step="0.000001" type="number" value={form.rateLimit5hUsd} />
            <Field label="1 天消费限额（USD）" min="0" onChange={(event) => updateForm("rateLimit1dUsd", event.currentTarget.value)} step="0.000001" type="number" value={form.rateLimit1dUsd} />
            <Field label="7 天消费限额（USD）" min="0" onChange={(event) => updateForm("rateLimit7dUsd", event.currentTarget.value)} step="0.000001" type="number" value={form.rateLimit7dUsd} />
          </div>
          <div className="key-form-grid">
            <Field label="IP 白名单" multiline onChange={(event) => updateForm("ipWhitelist", event.currentTarget.value)} placeholder="每行一个 IP 或 CIDR" rows={3} value={form.ipWhitelist} />
            <Field label="IP 黑名单" multiline onChange={(event) => updateForm("ipBlacklist", event.currentTarget.value)} placeholder="每行一个 IP 或 CIDR" rows={3} value={form.ipBlacklist} />
          </div>
        </div>
      </details>
    </form>
  );

  return (
    <section className="keys-panel panel" data-slide="C-API-03 C-API-04 C-API-05">
      <header className="keys-panel__header panel-title">
        <div className="keys-panel__title">
          <h2>API Keys</h2>
          <div className="keys-endpoint">
            <span>API Endpoint</span>
            {endpoint ? <code>{endpoint}</code> : <span>暂不可用</span>}
            <Tooltip compact content="复制 Endpoint">
              <Button aria-label="复制 Endpoint" disabled={!endpoint} onClick={() => void copyText(endpoint, "Endpoint 已复制")} size="sm" uniform variant="ghost">
                <Copy aria-hidden="true" size={16} />
              </Button>
            </Tooltip>
          </div>
        </div>
        <div className="keys-panel__header-actions">
          <Tooltip compact content="刷新 API Keys">
            <Button aria-label="刷新 API Keys" disabled={loading || referenceLoading} onClick={() => void refreshAll()} size="sm" uniform variant="outline">
              <RefreshCw aria-hidden="true" size={16} />
            </Button>
          </Tooltip>
          <Button color="primary" disabled={!groups.length || !csrfToken} onClick={openCreate} size="sm">
            <Plus aria-hidden="true" size={16} />创建 Key
          </Button>
        </div>
      </header>

      <form className="keys-filters" onSubmit={submitSearch}>
        <Field label="搜索 Key" maxLength={100} onChange={(event) => changeFilter("search", event.currentTarget.value)} placeholder="名称或 ID" value={query.search} />
        <Select label="分组筛选" onChange={(value) => changeFilter("groupId", value)} options={[{ value: "", label: "全部分组" }, { value: "0", label: "未分组" }, ...groups.map((group) => ({ value: group.id, label: group.name, description: groupMetadataLabel(group) }))]} value={query.groupId} />
        <Select label="状态筛选" onChange={(value) => changeFilter("status", value as KeyQuery["status"])} options={[{ value: "", label: "全部状态" }, { value: "active", label: "启用" }, { value: "disabled", label: "停用" }, { value: "quota_exhausted", label: "额度用尽" }, { value: "expired", label: "已过期" }]} value={query.status} />
        <Select label="排序" onChange={(value) => changeFilter("sortBy", value as KeyQuery["sortBy"])} options={[{ value: "createdAt", label: "创建时间" }, { value: "name", label: "名称" }, { value: "id", label: "ID" }, { value: "currentConcurrency", label: "当前并发" }, { value: "expiresAt", label: "过期时间" }, { value: "status", label: "状态" }, { value: "lastUsedAt", label: "最近使用" }]} value={query.sortBy} />
        <Select label="顺序" onChange={(value) => changeFilter("sortOrder", value as KeyQuery["sortOrder"])} options={[{ value: "desc", label: "降序" }, { value: "asc", label: "升序" }]} value={query.sortOrder} />
        <Select label="每页" onChange={(value) => changeFilter("pageSize", Number(value))} options={[10, 20, 50, 100].map((size) => ({ value: String(size), label: String(size) }))} value={String(query.pageSize)} />
        <Button className="keys-filter-submit" type="submit" variant="outline"><Search aria-hidden="true" size={16} />查询</Button>
      </form>

      {referenceError ? <Alert actions={<Button onClick={() => void loadReferenceData()} size="sm" variant="ghost">重试</Button>} color="warning" description={referenceError} title="参考配置部分不可用" /> : null}
      {groupsSource?.status === "unavailable" ? <Alert color="warning" description={`无法确认可用分组，已暂停创建和换组。原因代码：${groupsSource.reasonCode}`} title="分组暂不可用" /> : null}
      {endpointSource?.status === "unavailable" ? <Alert color="warning" description={`无法确认公共模型 Endpoint，未展示推测值。原因代码：${endpointSource.reasonCode}`} title="Endpoint 暂不可用" /> : null}
      {notice ? <Alert color="success" description={notice} title="操作完成" /> : null}
      {error ? <Alert actions={<Button onClick={() => void refreshAll()} size="sm" variant="ghost">刷新确认</Button>} color="danger" description={error} title="操作未确认" /> : null}

      {revealed ? (
        <div className="keys-secret" role="status">
          <div>
            <span>{revealed.name} · 完整 Key</span>
            <code>{revealed.value}</code>
            <small>仅保存在当前页面内存，60 秒后自动隐藏。</small>
          </div>
          <div className="keys-secret__actions">
            <Button onClick={() => void copyText(revealed.value, "Key 已复制")} size="sm" variant="outline"><Copy aria-hidden="true" size={16} />复制</Button>
            <Button aria-label="隐藏 Key" onClick={clearSecret} size="sm" uniform variant="ghost"><EyeOff aria-hidden="true" size={16} /></Button>
          </div>
        </div>
      ) : null}

      <SourceState
        empty={source?.status === "empty"}
        emptyDescription="创建普通 Key 后即可设置分组、额度与访问限制。"
        error={error && !source ? error : ""}
        loading={loading}
        onRetry={() => void refreshAll()}
        source={source}
        unavailableTitle="API Keys 暂不可用"
      >
        {(data) => (
          <>
            <div className="table-wrap keys-table-wrap">
              <table className="keys-table">
                <thead>
                  <tr><th>名称</th><th>分组 / 平台</th><th>状态</th><th>配额 / 用量</th><th>5h / 1d / 7d 消费限额</th><th>过期</th><th>最近使用</th><th>创建时间</th><th>操作</th></tr>
                </thead>
                <tbody>
                  {data.items.map((key) => {
                    const quotaProgress = key.quotaUsdMicros > 0 ? Math.min(100, Math.max(0, key.quotaUsedUsdMicros / key.quotaUsdMicros * 100)) : null;
                    const manageable = canManage(key);
                    return (
                      <tr key={key.id}>
                        <td><strong>{key.name}</strong><small>#{key.id} · {isProtectedWorkspaceKey(key) ? "Workspace 系统 Key" : "普通 Key"}</small></td>
                        <td>
                          {manageable ? (
                            <Select aria-label="快捷换组" disabled={busy || !groups.length} onChange={(groupId) => groupId !== key.groupId && void runKeyMutation(key, { groupId }, "分组已更新")} options={groups.map((group) => ({ value: group.id, label: group.name }))} value={key.groupId || ""} />
                          ) : <strong>{groupName(key.groupId)}</strong>}
                          <small>{groupMeta(key.groupId)}</small>
                        </td>
                        <td><Badge color={statusColor(key.status)}>{statusLabel(key.status)}</Badge><small>{key.currentConcurrency} 并发</small></td>
                        <td><strong>{key.quotaUsdMicros ? formatUsdMicros(key.quotaUsdMicros) : "不限"}</strong><small>已用 {formatUsdMicros(key.quotaUsedUsdMicros)}</small>{quotaProgress !== null ? <progress aria-label={`${key.name} 配额使用进度`} max="100" value={quotaProgress} /> : null}</td>
                        <td><strong>{formatUsdMicros(key.rateLimit5hUsdMicros)} / {formatUsdMicros(key.rateLimit1dUsdMicros)} / {formatUsdMicros(key.rateLimit7dUsdMicros)}</strong><small>已用 {formatUsdMicros(key.usage5hUsdMicros)} / {formatUsdMicros(key.usage1dUsdMicros)} / {formatUsdMicros(key.usage7dUsdMicros)}</small></td>
                        <td>{key.expiresAt ? formatDate(key.expiresAt, true) : "永不过期"}</td>
                        <td><strong>{key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "尚未使用"}</strong>{key.lastUsedIp ? <small>{key.lastUsedIp}</small> : null}</td>
                        <td>{key.createdAt ? formatDate(key.createdAt, true) : "-"}</td>
                        <td>{renderKeyActions(key)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div aria-label="API Key 列表" className="mobile-key-list">
              {data.items.map((key) => {
                const quotaProgress = key.quotaUsdMicros > 0 ? Math.min(100, Math.max(0, key.quotaUsedUsdMicros / key.quotaUsdMicros * 100)) : null;
                const manageable = canManage(key);
                return (
                  <article className="mobile-key-card" key={key.id}>
                    <header className="mobile-key-card__header">
                      <div className="mobile-key-card__identity">
                        <strong>{key.name}</strong>
                        <small>#{key.id} · {isProtectedWorkspaceKey(key) ? "Workspace 系统 Key" : "普通 Key"}</small>
                      </div>
                      <div className="mobile-key-card__status">
                        <Badge color={statusColor(key.status)}>{statusLabel(key.status)}</Badge>
                        <small>{key.currentConcurrency} 并发</small>
                      </div>
                    </header>
                    <dl className="mobile-key-card__facts">
                      <div className="mobile-key-card__wide">
                        <dt>分组 / 平台</dt>
                        <dd>
                          {manageable ? (
                            <Select aria-label="快捷换组" disabled={busy || !groups.length} onChange={(groupId) => groupId !== key.groupId && void runKeyMutation(key, { groupId }, "分组已更新")} options={groups.map((group) => ({ value: group.id, label: group.name }))} value={key.groupId || ""} />
                          ) : <strong>{groupName(key.groupId)}</strong>}
                          <small>{groupMeta(key.groupId)}</small>
                        </dd>
                      </div>
                      <div>
                        <dt>配额 / 用量</dt>
                        <dd><strong>{key.quotaUsdMicros ? formatUsdMicros(key.quotaUsdMicros) : "不限"}</strong><small>已用 {formatUsdMicros(key.quotaUsedUsdMicros)}</small>{quotaProgress !== null ? <progress aria-label={`${key.name} 配额使用进度`} max="100" value={quotaProgress} /> : null}</dd>
                      </div>
                      <div>
                        <dt>过期时间</dt>
                        <dd><strong>{key.expiresAt ? formatDate(key.expiresAt, true) : "永不过期"}</strong></dd>
                      </div>
                      <div className="mobile-key-card__wide">
                        <dt>5h / 1d / 7d 消费限额</dt>
                        <dd><strong>{formatUsdMicros(key.rateLimit5hUsdMicros)} / {formatUsdMicros(key.rateLimit1dUsdMicros)} / {formatUsdMicros(key.rateLimit7dUsdMicros)}</strong><small>已用 {formatUsdMicros(key.usage5hUsdMicros)} / {formatUsdMicros(key.usage1dUsdMicros)} / {formatUsdMicros(key.usage7dUsdMicros)}</small></dd>
                      </div>
                      <div>
                        <dt>最近使用</dt>
                        <dd><strong>{key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "尚未使用"}</strong>{key.lastUsedIp ? <small>{key.lastUsedIp}</small> : null}</dd>
                      </div>
                      <div>
                        <dt>创建时间</dt>
                        <dd><strong>{key.createdAt ? formatDate(key.createdAt, true) : "-"}</strong></dd>
                      </div>
                    </dl>
                    {revealed?.id === key.id ? (
                      <div className="mobile-key-secret">
                        <code>{revealed.value}</code>
                        <Button aria-label="复制 Key" onClick={() => void copyText(revealed.value, "Key 已复制")} size="sm" uniform variant="outline"><Copy aria-hidden="true" size={16} /></Button>
                      </div>
                    ) : null}
                    <footer className="mobile-key-card__footer">{renderKeyActions(key, "keys-row-actions mobile-key-actions")}</footer>
                  </article>
                );
              })}
            </div>
            <footer className="keys-pagination">
              <span>共 {data.total} 条</span>
              <Button aria-label="上一页" disabled={query.page <= 1 || loading} onClick={() => changePage(query.page - 1)} size="sm" uniform variant="outline"><ChevronLeft aria-hidden="true" size={16} /></Button>
              <strong>{query.page} / {data.pages || 1}</strong>
              <Button aria-label="下一页" disabled={query.page >= data.pages || loading} onClick={() => changePage(query.page + 1)} size="sm" uniform variant="outline"><ChevronRight aria-hidden="true" size={16} /></Button>
            </footer>
          </>
        )}
      </SourceState>

      <Modal
        className="keys-modal"
        footer={dialog === "key" ? <><Button disabled={busy} onClick={closeDialog} variant="outline">取消</Button><Button busy={busy} color="primary" disabled={!form.name.trim() || !form.groupId} form="keys-form-submit" type="submit"><Save aria-hidden="true" size={16} />{editingKey ? "保存" : "创建"}</Button></> : undefined}
        onClose={closeDialog}
        open={dialog === "key"}
        title={editingKey ? "编辑 API Key" : "创建 API Key"}
      >
        {keyForm}
      </Modal>

      <Modal
        className="keys-modal keys-modal--confirm"
        footer={<><Button disabled={busy} onClick={closeDialog} variant="outline">取消</Button><Button busy={busy} color="danger" onClick={() => void removeKey()}><Trash2 aria-hidden="true" size={16} />删除</Button></>}
        onClose={closeDialog}
        open={dialog === "delete"}
        title="删除 API Key"
      >
        <Alert color="danger" description={<>确认删除 <strong>{pendingDelete?.name}</strong>？该操作不可恢复。</>} title="删除后无法恢复" />
      </Modal>

      <Modal
        className="keys-modal keys-modal--use"
        footer={<Button onClick={closeDialog} variant="outline">关闭</Button>}
        onClose={closeDialog}
        open={dialog === "use"}
        title="API Key 使用说明"
      >
        <dl className="keys-use-facts">
          <div><dt>API Endpoint</dt><dd><code>{endpoint || "暂不可用"}</code></dd></div>
          <div><dt>分组平台</dt><dd><code>{useGroup?.platform || "暂不可用"}</code></dd></div>
          <div><dt>当前 Key</dt><dd><code>{revealed && useKey && revealed.id === useKey.id ? revealed.value : "已隐藏"}</code></dd></div>
        </dl>
        <div className="keys-code-block">
          <pre><code>{useConfiguration}</code></pre>
          <Button aria-label="复制配置" disabled={!useConfiguration} onClick={() => void copyText(useConfiguration, "配置已复制")} size="sm" uniform variant="ghost"><Copy aria-hidden="true" size={16} /></Button>
        </div>
      </Modal>
    </section>
  );
}

export default KeysPanel;
