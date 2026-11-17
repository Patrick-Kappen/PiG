package codingagent

import (
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

func TestToolResultEventOverrideCarriesTerminate(t *testing.T) {
	for _, flag := range []*bool{nil, new(false), new(true)} {
		got := ToolResultEventOverride(&extension.ToolResultEventResult{Terminate: flag})
		if got.Terminate != flag {
			t.Fatalf("termination override = %v, want %v", got.Terminate, flag)
		}
	}
}
