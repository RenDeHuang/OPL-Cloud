package schema

import "entgo.io/ent"

type AnnouncementRead struct{ ent.Schema }

func (AnnouncementRead) Fields() []ent.Field { return announcementReadFields() }
