package cli

import (
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/piglet"
)

// pig additive (D237): only members retained by the effective Piglet may load. The generated list is code supplied by main, never by YAML at run time.
func selectedDirectFactories(p *piglet.Piglet, factories []extension.NamedInlineExtension, disabled bool) []extension.InlineExtension {
	if disabled || p == nil {
		return nil
	}
	var selected []extension.InlineExtension
	for _, member := range p.Extensions {
		if member.Realization != "direct" {
			continue
		}
		for _, factory := range factories {
			if factory.Name == member.Name {
				selected = append(selected, factory)
				break
			}
		}
	}
	return selected
}
