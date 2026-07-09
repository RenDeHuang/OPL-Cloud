package schema

import "entgo.io/ent"

type Wallet struct{ ent.Schema }

func (Wallet) Fields() []ent.Field { return ledgerFields() }
