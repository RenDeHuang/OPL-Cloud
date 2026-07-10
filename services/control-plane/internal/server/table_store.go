package server

import "context"

type controlPlaneTableStore interface {
	ListUsers(ctx context.Context, includeDeleted bool) ([]map[string]any, error)
	SaveUser(ctx context.Context, row map[string]any) error
	DeleteUser(ctx context.Context, id string) error
	ListSessions(ctx context.Context) (controlPlaneRecordSet, error)
	SaveSession(ctx context.Context, row map[string]any) error
	DeleteSession(ctx context.Context, id string) error
	ListOrganizations(ctx context.Context) ([]map[string]any, error)
	SaveOrganization(ctx context.Context, row map[string]any) error
	ListMemberships(ctx context.Context) ([]map[string]any, error)
	SaveMembership(ctx context.Context, row map[string]any) error

	ListComputes(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveCompute(ctx context.Context, row map[string]any) error
	DeleteCompute(ctx context.Context, id string) error
	ListStorages(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveStorage(ctx context.Context, row map[string]any) error
	DeleteStorage(ctx context.Context, id string) error
	ListAttachments(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAttachment(ctx context.Context, row map[string]any) error
	DeleteAttachment(ctx context.Context, id string) error
	ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWorkspace(ctx context.Context, row map[string]any) error
	DeleteWorkspace(ctx context.Context, id string) error

	ListWallets(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWallet(ctx context.Context, row map[string]any) error
	ListLedger(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveLedgerEntry(ctx context.Context, row map[string]any) error
	ListWalletTransactions(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWalletTransaction(ctx context.Context, row map[string]any) error
	ListManualTopups(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveManualTopup(ctx context.Context, row map[string]any) error
	ListAuditEvents(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAuditEvent(ctx context.Context, row map[string]any) error
	ListSupportMappings(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveSupportMapping(ctx context.Context, row map[string]any) error
	ListRuntimeOperations(ctx context.Context) ([]map[string]any, error)
	SaveRuntimeOperation(ctx context.Context, row map[string]any) error
	ListProjectTaskSyncHeads(ctx context.Context) ([]map[string]any, error)
	SaveProjectTaskSyncHead(ctx context.Context, row map[string]any) error
	ListExecutionRequests(ctx context.Context) ([]map[string]any, error)
	SaveExecutionRequest(ctx context.Context, row map[string]any) error
	BillingReconciliation(ctx context.Context) (map[string]any, bool, error)
	SaveBillingReconciliation(ctx context.Context, row map[string]any) error
}
