package schema

import "entgo.io/ent"

type AdminAuditEvent struct{ ent.Schema }

func (AdminAuditEvent) Fields() []ent.Field { return commonFactFields() }
