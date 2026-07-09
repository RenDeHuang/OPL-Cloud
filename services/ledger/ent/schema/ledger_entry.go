package schema

import "entgo.io/ent"

type LedgerEntry struct{ ent.Schema }

func (LedgerEntry) Fields() []ent.Field { return ledgerFields() }
