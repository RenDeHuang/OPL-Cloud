package schema

import "entgo.io/ent"

type Workspace struct{ ent.Schema }

func (Workspace) Fields() []ent.Field { return commonFactFields() }
