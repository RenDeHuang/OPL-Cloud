package schema

import "entgo.io/ent"

type WorkspaceRuntimeAccess struct{ ent.Schema }

func (WorkspaceRuntimeAccess) Fields() []ent.Field { return fabricFields() }
