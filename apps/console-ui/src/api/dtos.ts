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

export interface MoneyDTO {
  currency: "USD";
  usdMicros: number;
}

export interface OperationStatusDTO {
  operationId: string;
  status: string;
  phase?: string;
  errorCode?: string;
  createdAt?: string;
  updatedAt?: string;
}

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

export type SessionDTO = AuthSession;
export type CurrentAccountDTO = AuthMeData;

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

export interface WorkspaceDTO extends Workspace {
  workspaceApiKeyId?: string;
}

export interface WorkspaceListData {
  items: WorkspaceDTO[];
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

export interface WorkspaceLaunchOperationDTO extends WorkspaceLaunchResponse {
  workspaceApiKeyId?: string;
  workspaceKeyStatus?: string;
  workspaceKeyFingerprint?: string;
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

export type WorkspaceAutoRenewRequest = WorkspaceRenewalRequest;
export type WorkspaceAutoRenewCommandDTO = WorkspaceRenewalResponse;

export interface RuntimeCheck {
  name: string;
  ok: boolean;
}

export interface RuntimeAccessSummary {
  username?: string;
  credentialStatus?: string;
  credentialVersion?: string;
}

export interface WorkspaceRuntimeDTO {
  workspaceId: string;
  status: "running" | "unready" | "not_found" | "destroyed";
  ready: boolean;
  checks: RuntimeCheck[];
  runtimeId?: string;
  url?: string;
  serviceName?: string;
  access?: RuntimeAccessSummary;
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

export type WorkspaceRuntimeCredentialDTO = RuntimeCredentialResponse;

export interface WorkspaceKeyRotationDTO extends OperationStatusDTO {
  workspaceId: string;
  previousKeyId?: string;
  workspaceApiKeyId: string;
  fingerprint: string;
}

export interface WorkspaceFileEntryDTO {
  name: string;
  relativePath: string;
  kind: "file" | "directory";
  sizeBytes?: number;
  updatedAt: string;
}

export interface WorkspaceFilePageDTO {
  path: string;
  items: WorkspaceFileEntryDTO[];
  nextCursor: string | null;
  sourceUpdatedAt?: string;
}

export interface WorkspaceFilesystemUsageDTO {
  totalBytes: number;
  usedBytes: number;
  availableBytes: number;
  measuredAt: string;
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

export type GatewayWalletDTO = GatewayWallet;

export interface CreateGatewayKeyRequest {
  name: string;
  groupId: string;
  ipWhitelist?: string[];
  ipBlacklist?: string[];
  quotaUsdMicros: number;
  expiresInDays?: number;
  rateLimit5hUsdMicros?: number;
  rateLimit1dUsdMicros?: number;
  rateLimit7dUsdMicros?: number;
}

export interface UpdateGatewayKeyRequest {
  name?: string;
  groupId?: string;
  ipWhitelist?: string[];
  ipBlacklist?: string[];
  quotaUsdMicros?: number;
  expiresAt?: string;
  rateLimit5hUsdMicros?: number;
  rateLimit1dUsdMicros?: number;
  rateLimit7dUsdMicros?: number;
  resetQuota?: boolean;
  resetRateLimitUsage?: boolean;
  enabled?: boolean;
}

export interface GatewayEndpointDTO {
  baseUrl: string;
}

export interface GatewayGroupDTO {
  id: string;
  name: string;
  description: string;
  platform: string;
  rateMultiplier: number;
  subscriptionType: string;
  status: string;
}

export interface GatewayGroupPageDTO {
  items: GatewayGroupDTO[];
  total: number;
}

export interface GatewayKeyListQuery {
  page?: number;
  pageSize?: number;
  search?: string;
  status?: "active" | "disabled" | "quota_exhausted" | "expired" | "";
  groupId?: string;
  sortBy?: "name" | "id" | "currentConcurrency" | "expiresAt" | "status" | "lastUsedAt" | "createdAt";
  sortOrder?: "asc" | "desc";
}

export interface GatewayKey {
  id: string;
  name: string;
  groupId: string | null;
  status: "active" | "disabled" | "quota_exhausted" | "expired";
  ipWhitelist: string[];
  ipBlacklist: string[];
  quotaUsdMicros: number;
  quotaUsedUsdMicros: number;
  rateLimit5hUsdMicros: number;
  rateLimit1dUsdMicros: number;
  rateLimit7dUsdMicros: number;
  usage5hUsdMicros: number;
  usage1dUsdMicros: number;
  usage7dUsdMicros: number;
  currentConcurrency: number;
  lastUsedAt: string | null;
  lastUsedIp: string | null;
  expiresAt: string | null;
  createdAt: string | null;
  updatedAt: string | null;
}

export interface GatewayKeySummaryDTO extends GatewayKey {
  kind: "general" | "workspace";
  manageable: boolean;
  deletable: boolean;
}

export interface GatewayKeyPageDTO {
  items: GatewayKeySummaryDTO[];
  total: number;
  page: number;
  pageSize: number;
  pages: number;
}

export interface GatewayKeysData {
  items: GatewayKey[];
  total: number;
}

export interface GatewayKeySecretDTO {
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

export interface GatewayKeyUsagePageDTO {
  items: GatewayUsageItem[];
  total: number;
  page: number;
  pageSize: number;
  pages: number;
}

export interface GatewayUsageSummaryDTO {
  totalRequests: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalTokens: number;
  totalActualCostUsdMicros: number;
}

export type GatewayAccountUsageSummaryDTO = GatewayUsageSummaryDTO;

export interface BalanceHistoryEntry {
  type: string;
  valueUsdMicros: number;
  status: string;
  usedAt: string | null;
  createdAt: string;
}

export interface GatewayBalanceHistoryPageDTO {
  items: BalanceHistoryEntry[];
  total: number;
  page: number;
  pageSize: number;
  pages: number;
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

export interface WorkspaceBillingReceiptDTO {
  receiptId: string;
  type: "billing.workspace_purchased.v1" | "billing.workspace_renewed.v1" |
    "billing.workspace_expired.v1" | "billing.workspace_refunded.v1";
  status: string;
  workspaceId: string;
  createdAt: string;
  priceVersion: string;
  currency: "USD";
  periodStart: string;
  paidThrough: string;
  totalUsdMicros: number;
  chargeReference?: string;
  components: {
    compute: { resourceType: "compute"; resourceId: string; chargeUsdMicros: number };
    storage: { resourceType: "storage"; resourceId: string; sizeGb: number; chargeUsdMicros: number };
  };
  fulfillment?: {
    computeAllocationId: string;
    storageId: string;
    attachmentId?: string;
    workspaceApiKeyId?: string;
    runtimeId?: string;
  };
  refundUsdMicros?: number;
}

export interface BillingReceiptPageDTO {
  receipts: WorkspaceBillingReceiptDTO[];
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

export interface OperatorUsageCostDTO {
  todayActualCostUsdMicros: number;
  totalActualCostUsdMicros: number;
  byPlatform?: Array<{
    platform: string;
    todayActualCostUsdMicros: number;
    totalActualCostUsdMicros: number;
  }>;
}

export interface OperatorAccountDTO extends OperatorAccount {
  gatewayIdentity: SourceEnvelope<{ userId: string; email: string; status: "active" | "disabled" }>;
  wallet: SourceEnvelope<GatewayWalletDTO>;
  keyCount: SourceEnvelope<number>;
  usage: SourceEnvelope<OperatorUsageCostDTO>;
  workspaceCount: SourceEnvelope<number>;
}

export interface OperatorAccountPageDTO {
  items: OperatorAccountDTO[];
  total: number;
  page: number;
  pageSize: number;
}

export interface ProvisionAccountRequest {
  email: string;
  password: string;
  name?: string;
}

export interface OperatorAccountCommandDTO extends OperationStatusDTO {
  accountId: string;
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

export interface OperatorResourceDTO {
  ownerAccount: SourceEnvelope<{ id: string }>;
  ownerUser: SourceEnvelope<{ id: string; email: string }>;
  workspace: SourceEnvelope<{ id: string; name?: string }>;
  resourceType: SourceEnvelope<string>;
  packageOrSpec: SourceEnvelope<string>;
  providerId: SourceEnvelope<string>;
  zone: SourceEnvelope<string>;
  status: SourceEnvelope<string>;
  createdAt: SourceEnvelope<string>;
  expiresAt: SourceEnvelope<string>;
  lastReadAt: SourceEnvelope<string>;
  operationRef: SourceEnvelope<string>;
  receiptRef: SourceEnvelope<string>;
}

export interface OperatorWorkspaceDTO {
  workspace: SourceEnvelope<WorkspaceDTO>;
  ownerAccount: SourceEnvelope<{ id: string }>;
  ownerUser: SourceEnvelope<{ id: string; email: string }>;
  resources: OperatorResourceDTO[];
  receipt: SourceEnvelope<WorkspaceBillingReceiptDTO>;
  workspaceKeyUsage: SourceEnvelope<OperatorUsageCostDTO & { keyId: string }>;
}

export interface OperatorWorkspacePageDTO {
  items: OperatorWorkspaceDTO[];
  total: number;
  page: number;
  pageSize: number;
}

export interface WalletAdjustmentRequest {
  kind: "recharge" | "debit" | "business_refund";
  amountUsd: string;
  reason: string;
  relatedOperationId?: string;
  confirmationAccountId: string;
}

export interface WalletAdjustmentRecoveryRequest {
  accountId: string;
  evidenceRef: string;
}

export interface WalletAdjustmentUpstreamFailureDTO {
  phase: string;
  httpStatus?: number;
  errorCode: string;
  requestId?: string;
}

export interface WalletAdjustmentOperationDTO extends OperationStatusDTO {
  accountId: string;
  kind: WalletAdjustmentRequest["kind"];
  amountUsd: string;
  reason: string;
  beforeBalance: SourceEnvelope<MoneyDTO>;
  afterBalance: SourceEnvelope<MoneyDTO>;
  balanceHistoryRef?: string;
  receiptId?: string;
  actor: string;
  relatedOperationId?: string;
  upstreamFailure?: WalletAdjustmentUpstreamFailureDTO;
  allowedActions?: Array<"recover_wallet_adjustment">;
}

export interface AnnouncementDTO {
  id: string;
  title: string;
  body: string;
  status: "draft" | "scheduled" | "published" | "withdrawn";
  startsAt?: string;
  endsAt?: string;
  publishedAt?: string;
  createdAt: string;
  updatedAt: string;
  read: boolean;
}

export interface AnnouncementPageDTO {
  items: AnnouncementDTO[];
  total: number;
  page: number;
  pageSize: number;
}

export interface AnnouncementReadDTO {
  announcementId: string;
  readAt: string;
}

export type OperatorAnnouncementPageDTO = AnnouncementPageDTO;

export interface AnnouncementDraftRequest {
  title: string;
  body: string;
  startsAt?: string;
  endsAt?: string;
}

export interface AnnouncementScheduleRequest {
  startsAt: string;
  endsAt?: string;
}

export interface ManagementState {
  users: AuthIdentity[];
  workspaces: Workspace[];
  computeAllocations: ResourceFact[];
  storageVolumes: ResourceFact[];
  storageAttachments: ResourceFact[];
}

export interface OperatorOverviewDTO {
  accounts: SourceEnvelope<{ total: number; active: number; disabled: number }>;
  wallet: SourceEnvelope<MoneyDTO>;
  keys: SourceEnvelope<{ total: number }>;
  usage: SourceEnvelope<OperatorUsageCostDTO>;
  workspaces: SourceEnvelope<{ total: number }>;
  resources: SourceEnvelope<{ total: number }>;
  reconciliation: SourceEnvelope<{ total: number }>;
  health: SourceEnvelope<OperatorHealthDTO>;
}

export interface OperatorReconciliationItemDTO {
  id: string;
  resourceType: "workspace" | "compute" | "storage";
  status: string;
  accountId: string;
  billingOperationId: string;
  phase: string;
  errorCode: string;
  allowedActions: Array<"recover_workspace_launch" | "resolve_billing_review">;
  operationRef?: string;
  receiptRef?: string;
}

export interface OperatorReconciliationPageDTO {
  items: OperatorReconciliationItemDTO[];
  total: number;
  page: number;
  pageSize: number;
}

export interface BillingReviewResolutionRequest {
  accountId: string;
  billingOperationId: string;
  decision: "activate_charged_resource" | "terminate_uncharged_absent" | "refund_charged_absent";
  evidenceRef: string;
}

export interface WorkspaceLaunchRecoveryRequest {
  accountId: string;
  billingOperationId: string;
  evidenceRef: string;
}

export interface ReadinessFact {
  ready?: boolean;
  generatedAt?: string;
  updatedAt?: string;
}

export interface OperatorHealthDTO {
  controlPlane: SourceEnvelope<ReadinessFact>;
  gateway: SourceEnvelope<ReadinessFact>;
  fabric: SourceEnvelope<ReadinessFact>;
  runtime: SourceEnvelope<ReadinessFact>;
  ledger: SourceEnvelope<ReadinessFact>;
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
