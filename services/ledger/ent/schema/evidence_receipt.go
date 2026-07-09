package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type EvidenceReceipt struct{ ent.Schema }

func (EvidenceReceipt) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("workspace_id").NotEmpty(),
		field.String("provider_request_id").NotEmpty(),
		field.String("redacted_url").Default(""),
		field.String("token_version").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		createdAtField(),
	}
}
