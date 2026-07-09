package schema

import "entgo.io/ent"

type ReconciliationReport struct{ ent.Schema }

func (ReconciliationReport) Fields() []ent.Field { return ledgerFields() }
