package ledger

import "context"

type Store interface {
	ManualTopUp(ctx context.Context, input ManualTopUpInput) (ManualTopUpResult, error)
	CreateHold(ctx context.Context, input HoldInput) (HoldResult, error)
	RecordEvidence(ctx context.Context, input EvidenceInput) (EvidenceReceipt, error)
	SettleResource(ctx context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error)
	Wallet(ctx context.Context, accountID string) (Wallet, error)
}
