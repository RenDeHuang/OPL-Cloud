package schema

import "entgo.io/ent"

type ArchivedAdminAuditEvent struct{ ent.Schema }

func (ArchivedAdminAuditEvent) Fields() []ent.Field { return commonFactFields() }
