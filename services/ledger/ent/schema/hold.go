package schema

import "entgo.io/ent"

type Hold struct{ ent.Schema }

func (Hold) Fields() []ent.Field { return ledgerFields() }
