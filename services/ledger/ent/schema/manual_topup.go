package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type ManualTopup struct{ ent.Schema }

func (ManualTopup) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("account_id").NotEmpty(),
		field.Int64("amount_cents"),
		field.String("currency").Default("CNY"),
		field.String("operator_user_id").Default(""),
		field.String("ledger_entry_id").NotEmpty(),
		field.String("wallet_transaction_id").NotEmpty(),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		field.String("reason").Default(""),
		createdAtField(),
	}
}
