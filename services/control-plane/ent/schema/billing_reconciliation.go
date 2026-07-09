package schema

import "entgo.io/ent"

type BillingReconciliation struct{ ent.Schema }

func (BillingReconciliation) Fields() []ent.Field { return commonFactFields() }
