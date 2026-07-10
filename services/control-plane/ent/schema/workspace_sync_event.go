package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type WorkspaceSyncEvent struct{ ent.Schema }

func (WorkspaceSyncEvent) Fields() []ent.Field { return workspaceSyncEventFields() }

func (WorkspaceSyncEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("workspace_id", "cursor").Unique(),
		index.Fields("workspace_id", "operation_id").Unique(),
		index.Fields("workspace_id", "entity_kind", "project_id", "task_id", "cursor"),
		index.Fields("workspace_id", "conflict_id"),
	}
}
