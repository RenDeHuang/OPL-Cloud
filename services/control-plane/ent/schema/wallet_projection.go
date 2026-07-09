package schema

import "entgo.io/ent"

type WalletProjection struct{ ent.Schema }

func (WalletProjection) Fields() []ent.Field { return commonFactFields() }
