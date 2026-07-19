export type SourceStatus = "available" | "empty" | "unavailable";
export type SourceValueStatus = Exclude<SourceStatus, "unavailable">;

export interface AvailableSource<T> {
  source: string;
  status: SourceValueStatus;
  available: true;
  fetchedAt: string;
  sourceUpdatedAt?: string;
  data: T;
}

export interface UnavailableSource {
  source: string;
  status: "unavailable";
  available: false;
  fetchedAt: string;
  sourceUpdatedAt?: string;
}

export type SourceEnvelope<T> = AvailableSource<T> | UnavailableSource;

export interface AuthIdentity {
  id: string;
  consoleUserId?: string;
  accountId: string;
  role: string;
  email: string;
  status: "active" | "disabled";
  name?: string;
  sub2apiUserId?: string;
}

export interface AuthSession {
  user: AuthIdentity;
  isOperator: boolean;
  csrfToken: string;
  expiresAt?: string;
}

export interface AuthMeData {
  consoleUserId: string;
  accountId: string;
  role: string;
  sub2apiUserId: string;
  email: string;
  status: "active" | "disabled";
}

export interface LoginRequest {
  email: string;
  password: string;
}

export interface Workspace {
  id: string;
  ownerAccountId: string;
  ownerUserId: string;
  state: string;
  createdAt: string;
  updatedAt: string;
  name?: string;
  url?: string;
  storageId?: string;
  currentComputeAllocationId?: string;
  currentAttachmentId?: string;
  runtimeId?: string;
  packageId?: "basic" | "pro";
  storageGb?: number;
  autoRenew?: boolean;
  priceVersion?: string;
  currency?: "USD";
  totalUsdMicros?: number;
  periodStart?: string;
  paidThrough?: string;
  renewalStatus?: string;
}

export interface WorkspaceListData {
  items: Workspace[];
  total: number;
}

export type PlanId = "basic" | "pro";

export interface WorkspaceLaunchRequest {
  name: string;
  packageId: PlanId;
  sizeGb: 10 | 100;
  autoRenew: false;
}

export interface WorkspaceLaunchResponse {
  operationId: string;
  status: string;
  phase: string;
  accountId: string;
  workspaceId?: string;
  name: string;
  packageId: PlanId;
  sizeGb: number;
  autoRenew: false;
  priceVersion: string;
  currency: "USD";
  totalChargeUsdMicros: number;
  computeAllocationId?: string;
  storageId?: string;
  attachmentId?: string;
  runtimeServiceName?: string;
  url?: string;
  receiptId?: string;
  errorCode?: string;
  createdAt?: string;
  updatedAt?: string;
}

export type WorkspaceLaunchListResponse = WorkspaceLaunchResponse[];

export interface WorkspaceRenewalRequest {
  autoRenew: boolean;
}

export interface WorkspaceRenewalResponse {
  autoRenew: boolean;
  effectiveAfter: string;
  nextRenewalAt: string;
  paidThrough: string;
  renewalStatus: string;
}

export interface RuntimeCheck {
  name: string;
  ok: boolean;
}

export interface RuntimeAccessSummary {
  username?: string;
  credentialStatus?: string;
  credentialVersion?: string;
}

export interface WorkspaceRuntimeStatus {
  workspaceId: string;
  status: "running" | "unready" | "not_found" | "destroyed";
  ready: boolean;
  checks: RuntimeCheck[];
  runtimeId?: string;
  url?: string;
  serviceName?: string;
  access?: RuntimeAccessSummary;
}

export interface WorkspaceRuntimeRequest {
  workspaceId: string;
}

export interface RuntimeCredentialAccess {
  account: string;
  username: string;
  password: string;
  credentialStatus: string;
  credentialVersion: string;
}

export type WorkspaceCredentialAccess = RuntimeCredentialAccess;

export interface RuntimeCredentialResponse {
  workspaceId: string;
  access: RuntimeCredentialAccess;
  receiptId?: string;
}

export interface PricingPlan {
  id: PlanId;
  name: string;
  available: boolean;
  cpu: number;
  memoryGb: number;
  diskGb: number;
  server: string;
  price: {
    priceVersion: string;
    currency: "USD";
    chargeUsdMicros: number;
  };
}

export interface PricingCatalogResponse {
  priceVersion: string;
  billingUnit: string;
  displayCurrency: "USD";
  walletCurrency: "USD";
  currency: "USD";
  packages: PricingPlan[];
}

export interface WorkspacePricePreview {
  resourceType: "workspace";
  priceVersion: string;
  packageId: PlanId;
  currency: "USD";
  displayCurrency: "USD";
  billingUnit: string;
  totalChargeUsdMicros: number;
}

export interface PricingPreviewRequest {
  resourceType: "workspace" | "compute" | "storage";
  packageId: PlanId;
  sizeGb?: number;
}

export interface PricingPreviewResponse {
  chargeUsdMicros?: number;
  resourceType: "workspace" | "compute" | "storage";
  packageId: PlanId;
  priceVersion: string;
  currency: "USD";
  totalChargeUsdMicros?: number;
  displayCurrency?: "USD";
  billingUnit?: string;
}

export interface GatewayWallet {
  userId: string;
  currency: "USD";
  usdMicros: number;
  status: string;
}

export interface GatewayKey {
  id: string;
  name: string;
  status: "active" | "disabled";
  quotaUsdMicros: number;
  quotaUsedUsdMicros: number;
  usage5hUsdMicros: number;
  usage1dUsdMicros: number;
  usage7dUsdMicros: number;
  lastUsedAt: string | null;
}

export interface GatewayKeysData {
  items: GatewayKey[];
  total: number;
}

export interface GatewayKeyReveal {
  id: string;
  name: string;
  status: "active" | "disabled";
  value: string;
}

export interface GatewayUsageItem {
  apiKeyId: string;
  requestId: string;
  createdAt: string;
  model: string;
  inboundEndpoint: string;
  requestType: string;
  inputTokens: number;
  outputTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  actualCostUsdMicros: number;
}

export interface GatewayUsageData {
  items: GatewayUsageItem[];
  total: number;
  page: number;
  pageSize: number;
  pages: number;
}

export interface GatewayUsageStats {
  totalRequests: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalTokens: number;
  totalActualCostUsdMicros: number;
}

export interface BalanceHistoryEntry {
  type: string;
  valueUsdMicros: number;
  status: string;
  usedAt: string | null;
  createdAt: string;
}

export interface BalanceHistoryData {
  items: BalanceHistoryEntry[];
  total: number;
}

export interface BillingReceipt {
  receiptId: string;
  type: string;
  status: string;
  workspaceId: string;
  createdAt: string;
  resourceType: string;
  resourceId: string;
  priceVersion: string;
  currency: "USD";
  periodStart: string;
  paidThrough: string;
  chargeUsdMicros?: number;
  totalUsdMicros?: number;
  refundUsdMicros?: number;
}

export interface BillingReceiptPage {
  receipts: BillingReceipt[];
  nextCursor: string;
  hasMore: boolean;
}

export interface OperatorAccount {
  accountId: string;
  consoleUserId: string;
  role: string;
  sub2apiUserId: string;
  email: string;
  status: "active" | "disabled";
}

export interface OperatorAccountsData {
  items: OperatorAccount[];
  total: number;
}

export interface CreateCustomerUserRequest {
  email: string;
  password: string;
  name?: string;
  accountId: string;
  role: "owner";
}

export interface ResourceFact {
  id: string;
  accountId?: string;
  workspaceId?: string;
  name?: string;
  status?: string;
  billingStatus?: string;
  updatedAt?: string;
  createdAt?: string;
  chargeUsdMicros?: number;
}

export interface ManagementState {
  users: AuthIdentity[];
  workspaces: Workspace[];
  computeAllocations: ResourceFact[];
  storageVolumes: ResourceFact[];
  storageAttachments: ResourceFact[];
}

export interface OperatorSummary {
  failedOperations: ResourceFact[];
  resourceAnomalies: ResourceFact[];
  notifications?: { total?: number };
}

export interface ReadinessFact {
  ready?: boolean;
  generatedAt?: string;
  updatedAt?: string;
}

export function decodeDto<T>(value: unknown): T {
  if (!value || typeof value !== "object") throw new Error("invalid_dto");
  return value as T;
}

export function decodeSource<T>(value: unknown): SourceEnvelope<T> {
  const dto = decodeDto<Record<string, unknown>>(value);
  if (dto.status === "unavailable" || dto.available === false) {
    return {
      source: String(dto.source || "unknown"),
      status: "unavailable",
      available: false,
      fetchedAt: String(dto.fetchedAt || "")
    };
  }
  if (dto.status !== "available" && dto.status !== "empty" || dto.available !== true || !("data" in dto)) {
    throw new Error("invalid_source_envelope");
  }
  return {
    source: String(dto.source || "unknown"),
    status: dto.status,
    available: true,
    fetchedAt: String(dto.fetchedAt || ""),
    ...(typeof dto.sourceUpdatedAt === "string" ? { sourceUpdatedAt: dto.sourceUpdatedAt } : {}),
    data: dto.data as T
  };
}
