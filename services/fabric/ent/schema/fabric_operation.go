package schema

import "entgo.io/ent"

type FabricOperation struct{ ent.Schema }

func (FabricOperation) Fields() []ent.Field { return fabricFields() }
