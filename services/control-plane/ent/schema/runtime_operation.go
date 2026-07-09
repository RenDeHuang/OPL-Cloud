package schema

import "entgo.io/ent"

type RuntimeOperation struct{ ent.Schema }

func (RuntimeOperation) Fields() []ent.Field { return commonFactFields() }
