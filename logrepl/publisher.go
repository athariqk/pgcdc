package logrepl

import pgcdcmodels "github.com/athariqk/pgcdc-models"

type Publisher interface {
	String() string
	Init(schema *Schema) error
	TryFullReplication(rows []*pgcdcmodels.Row) error
	OnBegin(xid uint32) error
	OnInsert(row pgcdcmodels.Row) error
	OnUpdate(row pgcdcmodels.Row) error
	OnDelete(row pgcdcmodels.Row) error
	OnCommit() error
}
