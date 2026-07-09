package schema

import "entgo.io/ent"

type WalletTransactionProjection struct{ ent.Schema }

func (WalletTransactionProjection) Fields() []ent.Field { return commonFactFields() }
