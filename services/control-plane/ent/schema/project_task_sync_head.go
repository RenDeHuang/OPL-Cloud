package schema

import "entgo.io/ent"

type ProjectTaskSyncHead struct{ ent.Schema }

func (ProjectTaskSyncHead) Fields() []ent.Field { return projectTaskSyncHeadFields() }
