package world

// Пин единого источника констант домена для сетки ячеек AoI (P4.1/F1):
// границы мира экспортированы из geo и попадают в NewGrid региона.

import (
	"testing"

	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/transport"
)

// TestGridUsesExportedGeoWorldBounds — сетка региона строится из
// экспортированных констант geo: левый нижний угол мира.
func TestGridUsesExportedGeoWorldBounds(t *testing.T) {
	t.Parallel()
	if geo.WorldMinX != -655360 || geo.WorldMinY != -589824 {
		t.Fatalf("geo.WorldMinX/Y = %d/%d; want −655360/−589824 (дрейф констант домена)",
			geo.WorldMinX, geo.WorldMinY)
	}
}

// TestRegionGridCoversDomainEdges — записи на краях домена и за ними:
// клетка записи = клетка сетки региона (кламп за доменом), нулевых паник
// и мусорных клеток нет.
func TestRegionGridCoversDomainEdges(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	edges := []Position{
		{X: geo.WorldMinX, Y: geo.WorldMinY, Z: 0},
		{X: -2147483647, Y: -2147483647, Z: 0}, // кламп к краевой клетке
		{X: 2147483647, Y: 2147483647, Z: 0},
		{X: -71338, Y: 258271, Z: -3104},
	}
	for i, pos := range edges {
		ent := &Entity{ID: transport.EntityID(i + 1), Owner: r.id, HP: 100, Pos: pos}
		rec := r.recordOf(ent)
		if rec.Cell != r.grid.CellOf(pos.X, pos.Y) {
			t.Errorf("позиция (%d,%d): Cell=%d; want %d (рассинхрон источника сетки)",
				pos.X, pos.Y, rec.Cell, r.grid.CellOf(pos.X, pos.Y))
		}
	}
}
