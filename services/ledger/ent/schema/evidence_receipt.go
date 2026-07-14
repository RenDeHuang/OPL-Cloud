package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type EvidenceReceipt struct{ ent.Schema }

func (EvidenceReceipt) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("receipt_type").Default(""),
		field.String("status").Default(""),
		field.String("account_id").Default(""),
		field.String("organization_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("project_id").Default(""),
		field.String("task_id").Default(""),
		field.String("job_id").Default(""),
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

func (EvidenceReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "created_at", "id"),
		index.Fields("organization_id", "created_at", "id"),
		index.Fields("workspace_id", "created_at", "id"),
		index.Fields("project_id", "created_at", "id"),
		index.Fields("task_id", "created_at", "id"),
		index.Fields("job_id", "created_at", "id"),
		index.Fields("receipt_type", "created_at", "id"),
		index.Fields("status", "created_at", "id"),
	}
}
