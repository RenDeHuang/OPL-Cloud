package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type FabricOperation struct{ ent.Schema }

func (FabricOperation) Fields() []ent.Field {
	fields := []ent.Field{
		idField(),
		field.String("operation_id").NotEmpty(),
		field.String("caller_service").NotEmpty(),
		field.String("action").NotEmpty(),
		field.String("resource_kind").NotEmpty(),
		field.String("resource_id").NotEmpty(),
		field.String("account_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("idempotency_key").Default(""),
		field.String("request_hash").Default(""),
		field.String("redacted_provider_payload").Default("{}"),
		field.String("status").NotEmpty(),
		field.String("error_code").Default(""),
		field.Bool("retryable").Default(false),
	}
	return append(fields, operationTimeFields()...)
}
