package schema

import "entgo.io/ent"

type Session struct{ ent.Schema }

func (Session) Fields() []ent.Field { return commonFactFields() }
