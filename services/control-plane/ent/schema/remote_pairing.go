package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type RemotePairing struct{ ent.Schema }

func (RemotePairing) Fields() []ent.Field { return remotePairingFields() }

func (RemotePairing) Indexes() []ent.Index {
	return []ent.Index{index.Fields("state", "reservation_expires_at", "seat_released", "id")}
}
