package schema

import "entgo.io/ent"

type ResourceSettlement struct{ ent.Schema }

func (ResourceSettlement) Fields() []ent.Field { return ledgerFields() }
