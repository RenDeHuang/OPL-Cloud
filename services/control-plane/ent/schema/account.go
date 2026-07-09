package schema

import "entgo.io/ent"

type Account struct{ ent.Schema }

func (Account) Fields() []ent.Field { return commonFactFields() }
