package ledger

import "context"

type Store interface {
	RecordReceipt(ctx context.Context, input ReceiptInput) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	UpdateReceiptRetention(ctx context.Context, input ReceiptRetentionInput) (ReceiptRetentionResult, error)
	PrivacyDeleteReceipt(ctx context.Context, input ReceiptPrivacyDeleteInput) (ReceiptRetentionResult, error)
	ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error)
	Continuation(ctx context.Context, receiptID string) (map[string]any, error)
	RecordArtifact(ctx context.Context, input ArtifactInput) (Artifact, error)
	Artifact(ctx context.Context, artifactID string) (Artifact, error)
	RecordReview(ctx context.Context, input ReviewInput) (Review, error)
	Review(ctx context.Context, reviewID string) (Review, error)
	CreateReviewPolicy(ctx context.Context, input ReviewPolicyInput) (ReviewPolicy, error)
	ReviewPolicy(ctx context.Context, policyID string) (ReviewPolicy, error)
	ListReviewPolicies(ctx context.Context, query ReviewPolicyQuery) ([]ReviewPolicy, error)
	EvaluateReviewGate(ctx context.Context, input ReviewGateInput) (ReviewGateResult, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error)
}
