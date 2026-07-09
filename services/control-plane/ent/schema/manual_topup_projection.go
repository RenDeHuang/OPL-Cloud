package schema

import "entgo.io/ent"

type ManualTopupProjection struct{ ent.Schema }

func (ManualTopupProjection) Fields() []ent.Field { return commonFactFields() }
