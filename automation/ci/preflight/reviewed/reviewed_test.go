// SPDX-License-Identifier: MIT

package reviewed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	commitA = "0123456789abcdef0123456789abcdef01234567"
	fileA   = Dir + "/0123456789/a/a_test.go.toml"
)

func entryText(commit, file string) string {
	return "[[entry]]\npath = \"" + file + "\"\ncommit = \"" + commit + "\"\nclass = \"NOBEHAVIOR\"\ncite = \"\"\nreason = \"r\"\n"
}

func TestEntryFile(t *testing.T) {
	for _, tc := range []struct {
		commit string
		want   string
	}{
		{commitA, fileA},
		{commitA[:10], fileA},
		{commitA[:12], fileA},
		{"01234", Dir + "/01234/a/a_test.go.toml"},
		{"", Dir + "//a/a_test.go.toml"},
	} {
		if got := (Entry{Path: "a/a_test.go", Commit: tc.commit}).File(); got != tc.want {
			t.Errorf("File() of commit %q = %s, want %s", tc.commit, got, tc.want)
		}
	}
	if !InDir(fileA) || InDir(Dir) || InDir(Dir+".toml") || InDir(Dir+"-old/x.toml") {
		t.Error("InDir holds exactly the files under Dir")
	}
}

func TestParseEntry(t *testing.T) {
	e, err := ParseEntry(fileA, entryText(commitA, "a/a_test.go"))
	if err != nil || e.Path != "a/a_test.go" || e.Commit != commitA || e.Class != "NOBEHAVIOR" {
		t.Fatalf("ParseEntry = %+v, %v", e, err)
	}
	for _, tc := range []struct {
		name string
		file string
		text string
		want string
	}{
		{"not TOML", fileA, "[[entry]\npath = ", fileA},
		{"an empty file", fileA, "", "holds 0 entries"},
		{"two entries", fileA, entryText(commitA, "a/a_test.go") + entryText(commitA, "a/a_test.go"), "holds 2 entries"},
		{"the file of another path", Dir + "/0123456789/a/b_test.go.toml", entryText(commitA, "a/a_test.go"), "belongs in " + fileA},
		{"the file of another commit", Dir + "/ffffffffff/a/a_test.go.toml", entryText(commitA, "a/a_test.go"), "belongs in " + fileA},
		{"a file outside the commit's directory", Dir + "/a_test.go.toml", entryText(commitA, "a/a_test.go"), "belongs in " + fileA},
		{"a path that leaves the directory", fileA, entryText(commitA, "../../a/a_test.go"), "belongs in "},
	} {
		if _, err := ParseEntry(tc.file, tc.text); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.file) {
			t.Errorf("%s: error %v, want one naming %s and %q", tc.name, err, tc.file, tc.want)
		}
	}
}

func TestParseFiles(t *testing.T) {
	fileB := Dir + "/0123456789/b/b_test.go.toml"
	entries, err := ParseFiles(map[string]string{fileB: entryText(commitA, "b/b_test.go"), fileA: entryText(commitA, "a/a_test.go")})
	if err != nil || len(entries) != 2 || entries[0].Path != "a/a_test.go" || entries[1].Path != "b/b_test.go" {
		t.Fatalf("ParseFiles = %+v, %v, want the entries in the order of the file names", entries, err)
	}
	if entries, err := ParseFiles(nil); err != nil || len(entries) != 0 {
		t.Fatalf("no file is no entry: %+v, %v", entries, err)
	}
	if _, err := ParseFiles(map[string]string{fileA: entryText(commitA, "a/a_test.go"), fileB: ""}); err == nil || !strings.Contains(err.Error(), fileB) {
		t.Fatalf("one unreadable file must be an error naming it: %v", err)
	}
}

func TestReadDir(t *testing.T) {
	root := t.TempDir()
	if entries, err := ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("a tree without the directory holds no entries: %+v, %v", entries, err)
	}
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commitB := "ffffffffff" + commitA[10:]
	write(fileA, entryText(commitA, "a/a_test.go"))
	write(Dir+"/ffffffffff/deep/er/b_test.go.toml", entryText(commitB, "deep/er/b_test.go"))
	entries, err := ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Commit != commitA || entries[1].Path != "deep/er/b_test.go" {
		t.Fatalf("ReadDir = %+v, %v", entries, err)
	}
	// Every file under the directory is an entry: one that is not fails, it is not skipped.
	write(Dir+"/README.md", "notes\n")
	if _, err := ReadDir(root); err == nil || !strings.Contains(err.Error(), Dir+"/README.md") {
		t.Fatalf("a file that is no entry must be an error naming it: %v", err)
	}
}

// TestReadDirFailsOnTheOldFile: a tree that still has the one-file ledger fails, and the error names the file of each
// entry the directory does not hold yet, so a merge that kept the file cannot leave rows that admit nothing.
func TestReadDirFailsOnTheOldFile(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fileB := Dir + "/0123456789/b/b_test.go.toml"
	write(fileA, entryText(commitA, "a/a_test.go"))
	for _, tc := range []struct {
		name, text string
		want, not  []string
	}{
		{"an entry the directory lacks", entryText(commitA, "a/a_test.go") + entryText(commitA, "b/b_test.go"), []string{OldFile, "after moving", fileB, "its other 1 entries"}, []string{fileA}},
		{"every entry is in the directory", entryText(commitA, "a/a_test.go"), []string{OldFile, "delete this file", "each of its 1 entries"}, []string{"moving"}},
		{"no entry", "", []string{OldFile, "delete this file", "each of its 0 entries"}, []string{"moving"}},
		{"not TOML", "[[entry\n", []string{OldFile, "delete this file", "not TOML"}, nil},
	} {
		write(OldFile, tc.text)
		_, err := ReadDir(root)
		if err == nil {
			t.Fatalf("%s: a tree with %s must fail", tc.name, OldFile)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: the error does not say %q: %v", tc.name, w, err)
			}
		}
		for _, w := range tc.not {
			if strings.Contains(err.Error(), w) {
				t.Errorf("%s: the error says %q: %v", tc.name, w, err)
			}
		}
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(OldFile))); err != nil {
		t.Fatal(err)
	}
	if entries, err := ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatalf("without the old file the tree holds the entry of the directory: %+v, %v", entries, err)
	}
}

func TestMatches(t *testing.T) {
	e := Entry{Path: "a/a_test.go", Commit: "0123456789"}
	full := "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name   string
		e      Entry
		commit string
		path   string
		want   bool
	}{
		{"prefix and path", e, full, "a/a_test.go", true},
		{"another path", e, full, "a/b_test.go", false},
		{"another commit", e, "ffffffffff" + full[10:], "a/a_test.go", false},
		{"prefix shorter than the minimum", Entry{Path: "a/a_test.go", Commit: "012345678"}, full, "a/a_test.go", false},
		{"no commit", Entry{Path: "a/a_test.go"}, full, "a/a_test.go", false},
	} {
		if got := tc.e.Matches(tc.commit, tc.path); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	piNames := map[string]bool{"agent.ts": true}
	recorded := func(id string) bool { return id == "D12" }
	for _, tc := range []struct {
		name string
		e    Entry
		want string
	}{
		{"CITE known", Entry{Class: "CITE", Cite: "packages/x/src/agent.ts:9", Reason: "r"}, ""},
		{"CITE unknown Pi file", Entry{Class: "CITE", Cite: "packages/x/src/invented.ts:9", Reason: "r"}, "does not name a Pi file"},
		{"CITE without a line", Entry{Class: "CITE", Cite: "packages/x/src/agent.ts", Reason: "r"}, "does not name a Pi file"},
		{"DIVERGENCE recorded", Entry{Class: "DIVERGENCE", Cite: "D12", Reason: "r"}, ""},
		{"DIVERGENCE without a record", Entry{Class: "DIVERGENCE", Cite: "D13", Reason: "r"}, "docs/parity/divergences/D13.md"},
		{"DIVERGENCE not an id", Entry{Class: "DIVERGENCE", Cite: "12", Reason: "r"}, "not a divergence id"},
		{"NOBEHAVIOR", Entry{Class: "NOBEHAVIOR", Reason: "r"}, ""},
		{"message-only", Entry{Class: "message-only", Reason: "r"}, ""},
		{"no reason", Entry{Class: "NOBEHAVIOR", Reason: " \t"}, "no reason"},
		{"unknown class", Entry{Class: "OTHER", Reason: "r"}, `unknown class "OTHER"`},
		{"empty class", Entry{Reason: "r"}, "unknown class"},
	} {
		err := tc.e.Validate(piNames, recorded)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want one naming %q", tc.name, err, tc.want)
		}
	}
}
