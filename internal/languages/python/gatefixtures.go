package python

import (
	"embed"
	"io/fs"

	"github.com/lernen-edu/lernen/internal/languages"
)

//go:embed all:gatefixtures
var gateFixturesFS embed.FS

func (a *Adapter) GateFixtures() (languages.GateFixtures, error) {
	sub, err := fs.Sub(gateFixturesFS, "gatefixtures")
	if err != nil {
		return languages.GateFixtures{}, err
	}
	return languages.LoadGateFixtures(sub)
}
