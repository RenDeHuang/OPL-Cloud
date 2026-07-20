package schema

import "entgo.io/ent"

type Announcement struct{ ent.Schema }

func (Announcement) Fields() []ent.Field { return announcementFields() }
