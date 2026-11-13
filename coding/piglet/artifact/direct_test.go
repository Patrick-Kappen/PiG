package artifact

import (
	"strings"
	"testing"
)

func TestDirectComponentIsGoBinaryOnly(t *testing.T) {
	base := ComponentInput{Kind: ComponentKindExtension, Name: "core", Language: "go", Direct: true, Origin: Origin{Source: "local:core", Digest: "sha256:" + strings.Repeat("a", 64)}}
	plan, err := BuildPlan([]ComponentInput{base})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Components[0].Realization != RealizationDirect || plan.Components[0].Materialization != MaterializationBinary {
		t.Fatal(plan)
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	invalid := plan.Components[0]
	invalid.Kind = ComponentKindMCP
	if err := validateResolvedComponent(invalid); err == nil {
		t.Fatal("decoded direct component accepted a non-extension kind")
	}
	for _, change := range []func(*ComponentInput){
		func(c *ComponentInput) { c.Language = "python" },
		func(c *ComponentInput) { c.IsolationRequired = true },
		func(c *ComponentInput) { c.External = true },
		func(c *ComponentInput) { c.Materialization = MaterializationRelease },
		func(c *ComponentInput) { c.Runtime = &RuntimeRequirement{Name: "go"} },
		func(c *ComponentInput) { c.Kind = ComponentKindMCP },
	} {
		input := base
		change(&input)
		if _, err := BuildPlan([]ComponentInput{input}); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
}
