package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type Session struct{ ent.Schema }

func (Session) Fields() []ent.Field { return sessionFields() }

func (Session) Indexes() []ent.Index { return []ent.Index{index.Fields("user_id")} }
