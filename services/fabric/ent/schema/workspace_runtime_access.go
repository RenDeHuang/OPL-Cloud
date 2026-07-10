package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type WorkspaceRuntimeAccess struct{ ent.Schema }

func (WorkspaceRuntimeAccess) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "fabric_workspace_runtime_access"}}
}

func (WorkspaceRuntimeAccess) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("workspace_id").NotEmpty().Unique(),
		field.String("runtime_id").NotEmpty(),
		field.String("url").NotEmpty(),
		field.String("service_name").Default(""),
		field.String("username").NotEmpty(),
		field.String("password").NotEmpty(),
		field.String("credential_status").NotEmpty(),
		field.String("credential_version").NotEmpty(),
		field.String("secret_ref").Default(""),
		updatedAtField(),
	}
}
