package ledger

import "context"

type Store interface {
	RecordReceipt(ctx context.Context, input ReceiptInput) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	UpdateReceiptRetention(ctx context.Context, input ReceiptRetentionInput) (ReceiptRetentionResult, error)
	PrivacyDeleteReceipt(ctx context.Context, input ReceiptPrivacyDeleteInput) (ReceiptRetentionResult, error)
	ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error)
}

type ReadinessStore interface {
	Ready(ctx context.Context) error
}
