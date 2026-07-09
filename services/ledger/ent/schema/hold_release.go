package schema

import "entgo.io/ent"

type HoldRelease struct{ ent.Schema }

func (HoldRelease) Fields() []ent.Field { return ledgerFields() }
