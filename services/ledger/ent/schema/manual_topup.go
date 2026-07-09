package schema

import "entgo.io/ent"

type ManualTopup struct{ ent.Schema }

func (ManualTopup) Fields() []ent.Field { return ledgerFields() }
