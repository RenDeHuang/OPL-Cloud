package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type Hold struct{ ent.Schema }

func (Hold) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("account_id").NotEmpty(),
		field.String("workspace_id").NotEmpty(),
		field.String("resource_type").NotEmpty(),
		field.String("resource_id").NotEmpty(),
		field.Int64("amount_cents"),
		field.String("currency").Default("CNY"),
		field.String("status").NotEmpty(),
		field.String("ledger_entry_id").NotEmpty(),
		field.String("wallet_transaction_id").NotEmpty(),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		createdAtField(),
	}
}
