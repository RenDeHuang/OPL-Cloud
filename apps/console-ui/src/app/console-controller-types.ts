import type {
  AnnouncementPageDTO,
  AuthSession,
  BillingReceipt,
  BillingReceiptPage,
  GatewayAccountUsageSummaryDTO,
  GatewayBalanceHistoryPageDTO,
  GatewayEndpointDTO,
  GatewayKeyPageDTO,
  GatewayKeySecretDTO,
  GatewayKeyUsagePageDTO,
  GatewayUsageSummaryDTO,
  GatewayWallet,
  OperatorAccountPageDTO,
  OperatorAnnouncementPageDTO,
  OperatorHealthDTO,
  OperatorOverviewDTO,
  OperatorReconciliationPageDTO,
  OperatorWorkspaceDTO,
  OperatorWorkspacePageDTO,
  PricingCatalogResponse,
  SourceEnvelope,
  WalletAdjustmentOperationDTO,
  WorkspaceCredentialAccess,
  WorkspaceDTO,
  WorkspaceGatewayBudgetDTO,
  WorkspaceLaunchResponse,
  WorkspaceListData,
  WorkspaceRuntimeDTO
} from "../api/dtos.ts";

export interface RemoteState<T> {
  value: T | null;
  loading: boolean;
  error: string;
}

export interface ConsoleSources {
  workspaces: RemoteState<SourceEnvelope<WorkspaceListData>>;
  workspaceDetail: RemoteState<SourceEnvelope<WorkspaceDTO | null>>;
  runtime: RemoteState<SourceEnvelope<WorkspaceRuntimeDTO>>;
  workspaceBudget: RemoteState<SourceEnvelope<WorkspaceGatewayBudgetDTO>>;
  wallet: RemoteState<SourceEnvelope<GatewayWallet>>;
  accountUsage: RemoteState<SourceEnvelope<GatewayAccountUsageSummaryDTO>>;
  balanceHistory: RemoteState<SourceEnvelope<GatewayBalanceHistoryPageDTO>>;
  receipts: RemoteState<SourceEnvelope<BillingReceiptPage>>;
  receiptDetail: RemoteState<SourceEnvelope<BillingReceipt>>;
  announcements: RemoteState<SourceEnvelope<AnnouncementPageDTO>>;
  catalog: RemoteState<PricingCatalogResponse>;
  usageKeys: RemoteState<SourceEnvelope<GatewayKeyPageDTO>>;
  usage: RemoteState<SourceEnvelope<GatewayKeyUsagePageDTO>>;
  usageSummary: RemoteState<SourceEnvelope<GatewayUsageSummaryDTO>>;
  endpoint: RemoteState<SourceEnvelope<GatewayEndpointDTO>>;
  operatorOverview: RemoteState<SourceEnvelope<OperatorOverviewDTO>>;
  operatorAccounts: RemoteState<SourceEnvelope<OperatorAccountPageDTO>>;
  operatorWorkspaces: RemoteState<SourceEnvelope<OperatorWorkspacePageDTO>>;
  operatorWorkspaceDetail: RemoteState<SourceEnvelope<OperatorWorkspaceDTO>>;
  operatorReconciliation: RemoteState<SourceEnvelope<OperatorReconciliationPageDTO>>;
  operatorHealth: RemoteState<SourceEnvelope<OperatorHealthDTO>>;
  operatorAnnouncements: RemoteState<SourceEnvelope<OperatorAnnouncementPageDTO>>;
}

export type AuthStatus = "public" | "checking" | "ready" | "error" | "logout_pending" | "logout_unconfirmed";
export type BillingView = "terms" | "receipts";
export type WorkspaceLaunchStep = "configure" | "confirm";
export type GlobalSlide = "account" | "support" | "";

export interface ConsoleSecrets {
  apiKey: GatewayKeySecretDTO | null;
  workspace: WorkspaceCredentialAccess | null;
}

export interface ConsoleTransientState {
  session: AuthSession | null;
  authStatus: AuthStatus;
  authError: string;
  toast: { text: string; tone: "good" | "danger" };
  globalSlide: GlobalSlide;
  launchOperation: WorkspaceLaunchResponse | null;
  walletAdjustmentOperation: WalletAdjustmentOperationDTO | null;
}
