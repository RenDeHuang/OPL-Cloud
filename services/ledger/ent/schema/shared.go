package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func idField() ent.Field {
	return field.String("id").NotEmpty().Unique()
}

func timeFields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func createdAtField() ent.Field {
	return field.Time("created_at").Default(time.Now)
}
