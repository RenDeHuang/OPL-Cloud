package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type Workspace struct{ ent.Schema }

func (Workspace) Fields() []ent.Field { return workspaceFields() }

func (Workspace) Indexes() []ent.Index {
	return []ent.Index{index.Fields("customer_product", "id")}
}
