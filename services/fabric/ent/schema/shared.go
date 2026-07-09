package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func fabricFields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique(),
		field.String("operation_id").Default(""),
		field.String("caller_service").Default(""),
		field.String("action").Default(""),
		field.String("resource_kind").Default(""),
		field.String("resource_id").Default(""),
		field.String("account_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("runtime_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("idempotency_key").Default(""),
		field.String("request_hash").Default(""),
		field.String("redacted_provider_payload").Default("{}"),
		field.String("status").Default(""),
		field.String("error_code").Default(""),
		field.Bool("retryable").Default(false),
		field.String("url").Default(""),
		field.String("service_name").Default(""),
		field.String("username").Default(""),
		field.String("password").Default(""),
		field.String("credential_status").Default(""),
		field.String("credential_version").Default(""),
		field.String("secret_ref").Default(""),
		field.Time("started_at").Default(time.Now),
		field.Time("finished_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
