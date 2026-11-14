package pigletbuild

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/coding/piglet"
)

func TestDirectMemberBuildsBesideFusedWithoutSDKFactory(t *testing.T) {
	root := t.TempDir()
	path := writeSmallPiglet(t, root)
	member := filepath.Join(root, "native")
	writeFixtureFile(t, filepath.Join(member, "go.mod"), "module example.com/native\n\ngo 1.26\nrequire github.com/MichaelKinsy/PiG v0.0.0\n")
	writeFixtureFile(t, filepath.Join(member, "extension.go"), `package native
import (
 "context"
 "fmt"
 "github.com/MichaelKinsy/PiG/coding/extension"
)
func DirectExtension(api extension.API) error {
 api.RegisterCommand("native-route", extension.CommandOptions{Description: fmt.Sprintf("native %T", api), Handler: func(context.Context, string) error { return nil }})
 api.OnSessionStart(func(context.Context, extension.SessionStartEvent) error { return nil })
 api.OnSessionShutdown(func(context.Context, extension.SessionShutdownEvent) error { return nil })
 return nil
}
`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("  - name: native\n    origins: [local:./native]\n    realization: direct\nbuild:\n  extensionRealization: fused\n")...)
	writeFixtureFile(t, path, string(data))
	p, err := piglet.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	cells, warnings := resolvePigletCells(p)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	plan := BuildPlan(extensionInputsFromCells(cells), Options{})
	if !Validate(plan, warnings, len(p.Extensions), len(plan.Ext), false, true).OK {
		t.Fatal(plan)
	}
	var decisions = map[string]Decision{}
	for _, member := range plan.Ext {
		decisions[member.Extension] = member.Decision
	}
	if decisions["native"] != DecisionDirect || decisions["hello"] != DecisionFuse {
		t.Fatal(decisions)
	}
	out := filepath.Join(root, "pig-direct")
	var stdout, stderr bytes.Buffer
	if code := runBuild([]string{path, "--format", "binary", "--builder", "native", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("build exit %d\n%s\n%s", code, &stdout, &stderr)
	}
	if !strings.Contains(stderr.String(), "Preparing direct Go members") || !strings.Contains(stderr.String(), "Checking linked Go members") {
		t.Fatalf("mixed build mislabels native members as fused: %s", &stderr)
	}
	home := t.TempDir()
	cmd := exec.CommandContext(t.Context(), out, "--mode", "rpc", "--offline", "--no-session")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+home, "PIG_HOME="+home, "PI_HOME="+home, "PIG_CODING_AGENT_DIR="+home, "PI_CODING_AGENT_DIR="+home)
	cmd.Stdin = strings.NewReader("{\"id\":\"commands\",\"type\":\"get_commands\"}\n")
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("native-route")) || !bytes.Contains(output, []byte("marker")) || !bytes.Contains(output, []byte("*factoryload.api")) {
		t.Fatalf("native and fused commands missing (%v): %s", err, output)
	}
}

func TestDirectVetAllowsNativeFactoryOnlyForDirectMembers(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		directOK     bool
	}{
		{"factory", `package hazard; import "github.com/MichaelKinsy/PiG/coding/extension"; var _ extension.ExtensionFactory`, true},
		{"loader", `package hazard; import "github.com/MichaelKinsy/PiG/coding/extension/factoryload"; var _ = factoryload.LoadExtensionFromFactory`, true},
		{"builtin", `package hazard; import "github.com/MichaelKinsy/PiG/coding/extension/builtin"; var _ = builtin.All`, false},
		{"exit", `package hazard; import "os"; var _ = os.Exit`, false},
		{"stdout", `package hazard; import "os"; var _ = os.Stdout`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFusedVetModule(t, tc.source)
			entries, err := collectFused(fusedVetCell(root))
			if err != nil {
				t.Fatal(err)
			}
			if err := vetFusedPackages(context.Background(), entries); err == nil {
				t.Fatal("fused accepted restricted symbol")
			}
			entries[0].Direct = true
			if err := vetFusedPackages(context.Background(), entries); (err == nil) != tc.directOK {
				t.Fatalf("direct vet: %v", err)
			}
		})
	}
}

func TestDirectRegistryDoesNotChangeFusedRegistry(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "go.mod"), "module github.com/MichaelKinsy/PiG\n\ngo 1.26\n")
	fusedRoot := writeFusedVetModule(t, "package hazard\nfunc Extension() {}\n")
	fused := fusedEntry{Name: "sdk", Pkg: "sdkmember", Package: "example.com/hazard", ModulePath: "example.com/hazard", Factory: "Extension", Root: fusedRoot}
	direct := fused
	direct.Direct, direct.Name, direct.Factory = true, "native", directFactory
	direct.Pkg = "nativemember"
	makeOverlay := func(entries []fusedEntry) *buildOverlay {
		o, err := newBuildOverlay(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := overlayFuse(o, root, entries, nil); err != nil {
			t.Fatal(err)
		}
		return o
	}
	mixed := makeOverlay([]fusedEntry{fused, direct})
	ordinary := makeOverlay([]fusedEntry{fused})
	registry := filepath.Join(root, "coding", "extension", "host", "fusepack", "registry_generated.go")
	a, err := os.ReadFile(mixed.replace[registry])
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ordinary.replace[registry])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || string(b) != renderFuseRegistry([]fusedEntry{fused}) {
		t.Fatal("fused registry changed")
	}
	native := filepath.Join(root, "cmd", "pig", "direct_factories.go")
	if _, ok := ordinary.replace[native]; ok {
		t.Fatal("fused-only build writes native registry")
	}
	data, err := os.ReadFile(mixed.replace[native])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "nativemember.DirectExtension") || strings.Contains(string(data), "init()") {
		t.Fatalf("registry: %s", data)
	}
}

func TestDirectSelectionRejectsNonGoAndMain(t *testing.T) {
	for _, source := range []string{"package main\nfunc main() {}", "not go"} {
		root := writeFusedVetModule(t, source)
		if _, err := resolveDirectConfig("native", root); err == nil {
			t.Fatal("invalid direct member accepted")
		}
	}
	if _, err := collectDirect(subprocess.CellSpec{Extensions: []subprocess.ExtConfig{{Name: "python", RuntimeLanguage: "python"}}}); err == nil {
		t.Fatal("non-Go direct accepted")
	}
}
