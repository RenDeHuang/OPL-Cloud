package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ContentTransfer struct{ ent.Schema }

func (ContentTransfer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "fabric_content_transfers"}}
}

func (ContentTransfer) Fields() []ent.Field {
	return []ent.Field{
		idField(), field.String("organization_id").NotEmpty(), field.String("workspace_id").NotEmpty(),
		field.String("project_id").NotEmpty(), field.String("path").NotEmpty(), field.String("digest").NotEmpty(),
		field.Int64("size"), field.Int("chunk_size"), field.Int("chunk_count"), field.String("status").NotEmpty(),
		field.String("idempotency_key").NotEmpty().Unique(), field.String("request_hash").NotEmpty(),
		createdAtField(), field.Time("completed_at").Optional().Nillable(),
	}
}

func (ContentTransfer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("digest"), index.Fields("workspace_id", "path")}
}

type ContentTransferChunk struct{ ent.Schema }

func (ContentTransferChunk) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "fabric_content_transfer_chunks"}}
}

func (ContentTransferChunk) Fields() []ent.Field {
	return []ent.Field{
		idField(), field.String("transfer_id").NotEmpty(), field.Int("chunk_index"),
		field.String("digest").NotEmpty(), field.Bytes("body"), createdAtField(),
	}
}

func (ContentTransferChunk) Indexes() []ent.Index {
	return []ent.Index{index.Fields("transfer_id", "chunk_index").Unique()}
}
