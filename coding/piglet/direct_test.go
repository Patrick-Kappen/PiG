package piglet

import "testing"

func TestPerExtensionDirectRealization(t *testing.T) {
	for _, realization := range []string{"direct", "fused", ""} {
		source := "name: native\nextensions:\n  - name: core\n    origins: [local:./core]\n"
		if realization != "" {
			source += "    realization: " + realization + "\n"
		}
		if _, err := ParseBytes([]byte(source)); err != nil {
			t.Errorf("parse %q: %v", realization, err)
		}
		if err := ValidateAgainstSchema([]byte(source)); err != nil {
			t.Errorf("schema %q: %v", realization, err)
		}
	}
	bad := []byte("name: native\nextensions:\n  - name: core\n    realization: native\n")
	if _, err := ParseBytes(bad); err == nil {
		t.Fatal("unsupported realization accepted")
	}
	if err := ValidateAgainstSchema(bad); err == nil {
		t.Fatal("schema accepts unsupported realization")
	}
}
