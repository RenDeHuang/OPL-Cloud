package schema

import "entgo.io/ent"

type StorageVolume struct{ ent.Schema }

func (StorageVolume) Fields() []ent.Field { return commonFactFields() }
