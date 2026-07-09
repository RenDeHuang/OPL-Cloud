package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func idField() ent.Field {
	return field.String("id").NotEmpty().Unique()
}

func createdAtField() ent.Field {
	return field.Time("created_at").Default(time.Now)
}

func updatedAtField() ent.Field {
	return field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now)
}

func operationTimeFields() []ent.Field {
	return []ent.Field{
		field.Time("started_at").Default(time.Now),
		field.Time("finished_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
	}
}
