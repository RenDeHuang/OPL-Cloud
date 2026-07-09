package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type IdempotencyKey struct{ ent.Schema }

func (IdempotencyKey) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("service").NotEmpty(),
		field.String("idempotency_key").NotEmpty(),
		field.String("request_hash").NotEmpty(),
		field.String("response_ref").Default(""),
		createdAtField(),
	}
}
