package cli

import (
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/piglet"
)

func TestDirectFactoriesRespectSelectionAndDisable(t *testing.T) {
	factories := []extension.NamedInlineExtension{{Name: "core"}, {Name: "other"}}
	for _, tc := range []struct {
		name     string
		p        *piglet.Piglet
		disabled bool
		want     int
	}{
		{name: "stock"},
		{name: "selected", p: &piglet.Piglet{Extensions: []piglet.ExtensionEntry{{Name: "core", Realization: "direct"}}}, want: 1},
		{name: "disabled", p: &piglet.Piglet{Extensions: []piglet.ExtensionEntry{{Name: "core", Realization: "direct"}}}, disabled: true},
		{name: "fused", p: &piglet.Piglet{Extensions: []piglet.ExtensionEntry{{Name: "core"}}}},
		{name: "removed", p: &piglet.Piglet{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectedDirectFactories(tc.p, factories, tc.disabled)
			if len(got) != tc.want {
				t.Fatalf("got %v, want %d", got, tc.want)
			}
		})
	}
}
