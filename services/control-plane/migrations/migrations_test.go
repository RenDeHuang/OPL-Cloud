package migrations

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
)

type recordingDriver struct {
	dialect.Driver
	query string
}

func (d *recordingDriver) Exec(_ context.Context, query string, _ any, _ any) error {
	d.query = query
	return nil
}

func TestApplyExecutesEmbeddedMonthlyHardCut(t *testing.T) {
	driver := &recordingDriver{}
	if err := Apply(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"DROP TABLE IF EXISTS control_plane_wallet_projections",
		"ADD COLUMN IF NOT EXISTS sub2api_user_id",
		"DROP COLUMN IF EXISTS hold_id",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded migration missing %q", required)
		}
	}
}

func TestApplySub2APIUserUniquenessFailsClosedAndAddsPartialIndex(t *testing.T) {
	driver := &recordingDriver{}
	if err := ApplySub2APIUserUniqueness(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"GROUP BY sub2api_user_id",
		"RAISE EXCEPTION 'duplicate sub2api_user_id mappings'",
		"CREATE UNIQUE INDEX",
		"WHERE sub2api_user_id > 0",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded mapping migration missing %q", required)
		}
	}
}

func TestApplyPrimaryWorkspaceAddsClassificationAndFailsClosedOnDuplicates(t *testing.T) {
	driver := &recordingDriver{}
	if err := ApplyPrimaryWorkspace(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS verification_slot_id",
		"ADD COLUMN IF NOT EXISTS customer_product",
		"duplicate primary Workspaces",
		"COALESCE(NULLIF(account_id, ''), owner_account_id)",
		"CREATE UNIQUE INDEX",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded primary Workspace migration missing %q", required)
		}
	}
}
