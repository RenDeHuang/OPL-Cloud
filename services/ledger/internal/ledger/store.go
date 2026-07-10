package ledger

import "context"

type Store interface {
	ManualTopUp(ctx context.Context, input ManualTopUpInput) (ManualTopUpResult, error)
	CreateHold(ctx context.Context, input HoldInput) (HoldResult, error)
	ReleaseHold(ctx context.Context, input HoldReleaseInput) (HoldReleaseResult, error)
	RecordReceipt(ctx context.Context, input ReceiptInput) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	Continuation(ctx context.Context, receiptID string) (map[string]any, error)
	RecordArtifact(ctx context.Context, input ArtifactInput) (Artifact, error)
	Artifact(ctx context.Context, artifactID string) (Artifact, error)
	RecordReview(ctx context.Context, input ReviewInput) (Review, error)
	Review(ctx context.Context, reviewID string) (Review, error)
	SettleResource(ctx context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error)
	Wallet(ctx context.Context, accountID string) (Wallet, error)
	ListLedgerEntries(ctx context.Context, accountID string) ([]LedgerEntry, error)
	ListWalletTransactions(ctx context.Context, accountID string) ([]WalletTransaction, error)
	ListManualTopUps(ctx context.Context, accountID string) ([]ManualTopUp, error)
	ListResourceSettlements(ctx context.Context, accountID string) ([]ResourceSettlementResult, error)
}
