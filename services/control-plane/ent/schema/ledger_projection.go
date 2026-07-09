package schema

import "entgo.io/ent"

type LedgerProjection struct{ ent.Schema }

func (LedgerProjection) Fields() []ent.Field { return commonFactFields() }
