package schema

import "entgo.io/ent"

type IdempotencyKey struct{ ent.Schema }

func (IdempotencyKey) Fields() []ent.Field { return ledgerFields() }
