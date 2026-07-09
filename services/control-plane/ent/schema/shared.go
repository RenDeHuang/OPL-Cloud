package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func commonFactFields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique(),
		field.String("account_id").Default(""),
		field.String("owner_account_id").Default(""),
		field.String("owner_user_id").Default(""),
		field.String("user_id").Default(""),
		field.String("email").Default(""),
		field.String("role").Default(""),
		field.String("status").Default(""),
		field.String("name").Default(""),
		field.String("workspace_id").Default(""),
		field.String("resource_id").Default(""),
		field.String("resource_kind").Default(""),
		field.String("operation_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_resource_id").Default(""),
		field.String("url").Default(""),
		field.String("hold_id").Default(""),
		field.String("hold_release_id").Default(""),
		field.String("ledger_entry_id").Default(""),
		field.String("wallet_transaction_id").Default(""),
		field.String("settlement_id").Default(""),
		field.String("pricing_version").Default(""),
		field.Int64("amount_cents").Default(0),
		field.Int64("balance_cents").Default(0),
		field.Int64("frozen_cents").Default(0),
		field.Int64("available_cents").Default(0),
		field.Int64("total_spent_cents").Default(0),
		field.Float("quantity").Default(0),
		field.String("unit").Default(""),
		field.String("reason").Default(""),
		field.String("result").Default(""),
		field.String("source").Default(""),
		field.String("direction").Default(""),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("archived_at").Optional().Nillable(),
	}
}
