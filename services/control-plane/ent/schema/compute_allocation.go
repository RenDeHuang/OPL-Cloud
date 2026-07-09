package schema

import "entgo.io/ent"

type ComputeAllocation struct{ ent.Schema }

func (ComputeAllocation) Fields() []ent.Field { return commonFactFields() }
