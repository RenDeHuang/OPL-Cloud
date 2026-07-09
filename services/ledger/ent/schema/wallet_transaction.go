package schema

import "entgo.io/ent"

type WalletTransaction struct{ ent.Schema }

func (WalletTransaction) Fields() []ent.Field { return ledgerFields() }
