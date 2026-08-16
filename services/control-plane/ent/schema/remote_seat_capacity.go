package schema

import "entgo.io/ent"

type RemoteSeatCapacity struct{ ent.Schema }

func (RemoteSeatCapacity) Fields() []ent.Field { return remoteSeatCapacityFields() }
