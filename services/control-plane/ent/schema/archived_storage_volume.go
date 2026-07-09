package schema

import "entgo.io/ent"

type ArchivedStorageVolume struct{ ent.Schema }

func (ArchivedStorageVolume) Fields() []ent.Field { return commonFactFields() }
