package schema

import "entgo.io/ent"

type ExecutionRequest struct{ ent.Schema }

func (ExecutionRequest) Fields() []ent.Field { return executionRequestFields() }
