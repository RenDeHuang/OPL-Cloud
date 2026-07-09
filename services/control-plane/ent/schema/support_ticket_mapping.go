package schema

import "entgo.io/ent"

type SupportTicketMapping struct{ ent.Schema }

func (SupportTicketMapping) Fields() []ent.Field { return commonFactFields() }
