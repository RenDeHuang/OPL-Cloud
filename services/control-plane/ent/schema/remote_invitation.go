package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type RemoteInvitation struct{ ent.Schema }

func (RemoteInvitation) Fields() []ent.Field { return remoteInvitationFields() }

func (RemoteInvitation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status", "expires_at", "id")}
}
