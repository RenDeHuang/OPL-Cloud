package schema

import "entgo.io/ent"

type ArchivedStorageAttachment struct{ ent.Schema }

func (ArchivedStorageAttachment) Fields() []ent.Field { return commonFactFields() }
