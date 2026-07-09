package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type ReconciliationReport struct{ ent.Schema }

func (ReconciliationReport) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("status").Default("ok"),
		field.String("report_json").Default("{}"),
		field.Bool("block_new_workspaces").Default(false),
		field.String("reason").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		createdAtField(),
	}
}
