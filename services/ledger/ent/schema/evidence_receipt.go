package schema

import "entgo.io/ent"

type EvidenceReceipt struct{ ent.Schema }

func (EvidenceReceipt) Fields() []ent.Field { return ledgerFields() }
