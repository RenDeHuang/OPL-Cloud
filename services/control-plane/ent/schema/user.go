package schema

import "entgo.io/ent"

type User struct{ ent.Schema }

func (User) Fields() []ent.Field { return commonFactFields() }
