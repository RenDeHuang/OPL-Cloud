package schema

import "entgo.io/ent"

type StorageAttachment struct{ ent.Schema }

func (StorageAttachment) Fields() []ent.Field { return commonFactFields() }
