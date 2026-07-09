package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type LedgerEntry struct{ ent.Schema }

func (LedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("account_id").NotEmpty(),
		field.Int64("amount_cents"),
		field.String("currency").Default("CNY"),
		field.String("direction").NotEmpty(),
		field.String("source").NotEmpty(),
		field.String("operator_user_id").Default(""),
		field.String("reason").Default(""),
		createdAtField(),
	}
}
