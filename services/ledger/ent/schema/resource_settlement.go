package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type ResourceSettlement struct{ ent.Schema }

func (ResourceSettlement) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("account_id").NotEmpty(),
		field.String("workspace_id").Default(""),
		field.String("resource_type").NotEmpty(),
		field.String("resource_id").NotEmpty(),
		field.String("hold_id").Default(""),
		field.Int64("amount_cents"),
		field.String("currency").Default("CNY"),
		field.String("status").NotEmpty(),
		field.String("ledger_entry_id").NotEmpty(),
		field.String("wallet_transaction_id").NotEmpty(),
		field.String("pricing_version").Default(""),
		field.String("price_snapshot_json").Default("{}"),
		field.String("usage_period_start").Default(""),
		field.String("usage_period_end").Default(""),
		field.Float("quantity").Default(0),
		field.String("unit").Default(""),
		field.String("provider_cost_evidence_ref").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		createdAtField(),
	}
}
