package schema

import "entgo.io/ent"

type Membership struct{ ent.Schema }

func (Membership) Fields() []ent.Field { return commonFactFields() }
