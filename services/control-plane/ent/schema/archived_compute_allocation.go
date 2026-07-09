package schema

import "entgo.io/ent"

type ArchivedComputeAllocation struct{ ent.Schema }

func (ArchivedComputeAllocation) Fields() []ent.Field { return commonFactFields() }
