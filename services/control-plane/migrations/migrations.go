package migrations

import (
	"context"
	_ "embed"

	"entgo.io/ent/dialect"
)

//go:embed 202607140001_sub2api_monthly_hard_cut.sql
var monthlyHardCut string

func Apply(ctx context.Context, driver dialect.Driver) error {
	return driver.Exec(ctx, monthlyHardCut, []any{}, nil)
}
