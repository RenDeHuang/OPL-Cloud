package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type RuntimeOperation struct{ ent.Schema }

func (RuntimeOperation) Fields() []ent.Field { return runtimeOperationFields() }

func (RuntimeOperation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("action", "status", "created_at", "id"),
		index.Fields("account_id", "action", "status", "created_at", "id"),
		index.Fields("workspace_id", "action", "status", "period_start", "created_at", "id"),
	}
}
