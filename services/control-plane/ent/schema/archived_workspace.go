package schema

import "entgo.io/ent"

type ArchivedWorkspace struct{ ent.Schema }

func (ArchivedWorkspace) Fields() []ent.Field { return commonFactFields() }
