package schema

import "entgo.io/ent"

type AuthAttempt struct{ ent.Schema }

func (AuthAttempt) Fields() []ent.Field { return commonFactFields() }
