package migrations

import (
	"context"
	_ "embed"

	"entgo.io/ent/dialect"
)

//go:embed 202607140001_sub2api_monthly_hard_cut.sql
var monthlyHardCut string

//go:embed 202607160001_sub2api_user_unique.sql
var sub2APIUserUnique string

//go:embed 202607160002_primary_workspace.sql
var primaryWorkspace string

//go:embed 202607170001_invited_account_identity.sql
var invitedAccountIdentity string

//go:embed 202607170002_workspace_renewal.sql
var workspaceRenewal string

//go:embed 202607170003_workspace_auto_renew_audit.sql
var autoRenewAudit string

func Apply(ctx context.Context, driver dialect.Driver) error {
	return driver.Exec(ctx, monthlyHardCut, []any{}, nil)
}

func ApplySub2APIUserUniqueness(ctx context.Context, driver dialect.Driver) error {
	return driver.Exec(ctx, sub2APIUserUnique, []any{}, nil)
}

func ApplyPrimaryWorkspace(ctx context.Context, driver dialect.Driver) error {
	return driver.Exec(ctx, primaryWorkspace, []any{}, nil)
}

func ApplyInvitedAccountIdentity(ctx context.Context, driver dialect.Driver) error {
	tx, err := driver.Tx(ctx)
	if err != nil {
		return err
	}
	if err := tx.Exec(ctx, invitedAccountIdentity, []any{}, nil); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func ApplyWorkspaceRenewal(ctx context.Context, driver dialect.Driver) error {
	tx, err := driver.Tx(ctx)
	if err != nil {
		return err
	}
	if err := tx.Exec(ctx, workspaceRenewal, []any{}, nil); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func ApplyAutoRenewAudit(ctx context.Context, driver dialect.Driver) error {
	return driver.Exec(ctx, autoRenewAudit, []any{}, nil)
}
