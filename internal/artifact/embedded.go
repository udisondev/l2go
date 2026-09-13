//go:build embedded

package artifact

import (
	_ "embed"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// embeddedArtifact — байты артефакта, вшитые сборкой с тегом embedded.
//
//go:embed embedded/artifact.l2a
var embeddedArtifact []byte

// LoadEmbedded загружает статику из байтов, вшитых в бинарарь.
func LoadEmbedded() (*data.Static, *geo.Map, *Meta, error) {
	return Decode(embeddedArtifact)
}
