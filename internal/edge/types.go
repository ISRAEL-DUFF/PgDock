package edge

import "github.com/israel-duff/pgdock/internal/datacat"

// The catalog types, from internal/datacat.
type (
	Catalog    = datacat.Catalog
	Table      = datacat.Table
	Column     = datacat.Column
	ForeignKey = datacat.ForeignKey
)

const (
	kindTable   = datacat.KindTable
	kindPart    = datacat.KindPart
	kindView    = datacat.KindView
	kindMatView = datacat.KindMatView
	kindForeign = datacat.KindForeign
)
