package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/index"
)

type RemoteDeviceCredential struct{ ent.Schema }

func (RemoteDeviceCredential) Fields() []ent.Field { return remoteDeviceCredentialFields() }

func (RemoteDeviceCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("pairing_id", "device_id").Unique(),
		index.Fields("pairing_id", "status"),
	}
}
