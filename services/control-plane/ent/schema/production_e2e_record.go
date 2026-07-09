package schema

import "entgo.io/ent"

type ProductionE2ERecord struct{ ent.Schema }

func (ProductionE2ERecord) Fields() []ent.Field { return commonFactFields() }
