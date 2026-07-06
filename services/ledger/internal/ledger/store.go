package ledger

import "context"

type Store interface {
	ManualTopUp(ctx context.Context, input ManualTopUpInput) (ManualTopUpResult, error)
	Wallet(ctx context.Context, accountID string) (Wallet, error)
}
