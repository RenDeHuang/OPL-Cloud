package schema

import "entgo.io/ent"

type ArchiveJob struct{ ent.Schema }

func (ArchiveJob) Fields() []ent.Field { return commonFactFields() }
