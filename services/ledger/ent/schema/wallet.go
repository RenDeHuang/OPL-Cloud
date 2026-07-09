package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type Wallet struct{ ent.Schema }

func (Wallet) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("account_id").NotEmpty().Unique(),
		field.Int64("balance_cents").Default(0),
		field.Int64("frozen_cents").Default(0),
		field.Int64("available_cents").Default(0),
		field.Int64("total_spent_cents").Default(0),
		field.String("currency").Default("CNY"),
		field.Time("updated_at"),
	}
}
