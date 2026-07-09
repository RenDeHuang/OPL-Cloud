package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func ledgerFields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique(),
		field.String("account_id").Default(""),
		field.Int64("balance_cents").Default(0),
		field.Int64("frozen_cents").Default(0),
		field.Int64("available_cents").Default(0),
		field.Int64("total_spent_cents").Default(0),
		field.Int64("amount_cents").Default(0),
		field.String("currency").Default("CNY"),
		field.String("direction").Default(""),
		field.String("source").Default(""),
		field.String("operator_user_id").Default(""),
		field.String("reason").Default(""),
		field.String("ledger_entry_id").Default(""),
		field.String("wallet_transaction_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("resource_type").Default(""),
		field.String("resource_id").Default(""),
		field.String("hold_id").Default(""),
		field.String("status").Default(""),
		field.String("pricing_version").Default(""),
		field.String("price_snapshot_json").Default("{}"),
		field.String("usage_period_start").Default(""),
		field.String("usage_period_end").Default(""),
		field.Float("quantity").Default(0),
		field.String("unit").Default(""),
		field.String("provider_cost_evidence_ref").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("redacted_url").Default(""),
		field.String("token_version").Default(""),
		field.String("idempotency_key").Default(""),
		field.String("request_hash").Default(""),
		field.String("report_json").Default("{}"),
		field.Bool("block_new_workspaces").Default(false),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
