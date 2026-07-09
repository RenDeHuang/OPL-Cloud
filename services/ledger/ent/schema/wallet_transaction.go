package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type WalletTransaction struct{ ent.Schema }

func (WalletTransaction) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("account_id").NotEmpty(),
		field.String("ledger_entry_id").NotEmpty(),
		field.Int64("amount_cents"),
		field.Int64("balance_cents"),
		field.Int64("frozen_cents"),
		field.Int64("available_cents"),
		field.Int64("total_spent_cents"),
		field.String("currency").Default("CNY"),
		createdAtField(),
	}
}
