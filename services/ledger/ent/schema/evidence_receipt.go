package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type EvidenceReceipt struct{ ent.Schema }

func (EvidenceReceipt) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("receipt_type").Default(""),
		field.String("status").Default(""),
		field.String("workspace_id").Default(""),
		field.String("payload_json").Default("{}"),
		field.String("supersedes_receipt_id").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("redacted_url").Default(""),
		field.String("token_version").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		createdAtField(),
	}
}
