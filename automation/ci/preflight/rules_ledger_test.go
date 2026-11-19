// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/MichaelKinsy/PiG/automation/ci/preflight/reviewed"
)

const (
	ledgerCommitA = "aaaaaaaaaa1111111111111111111111111111aa"
	ledgerCommitB = "bbbbbbbbbb2222222222222222222222222222bb"
	ledgerTestA   = "a/a_test.go"
	ledgerTestB   = "b/b_test.go"
)

func ledgerRow(commit, file, class, cite, reason string) string {
	return fmt.Sprintf("[[entry]]\npath = %q\ncommit = %q\nclass = %q\ncite = %q\nreason = %q\n", file, commit, class, cite, reason)
}

// ledgerOf is the ledger a PR holds for the rows of text, which an empty line parts: each row in the file of its commit
// and path. A part that is not a row is the text of the file of ledgerCommitA and ledgerTestA.
func ledgerOf(text string) map[string]string {
	if text == "" {
		return nil
	}
	files := map[string]string{}
	for part := range strings.SplitSeq(text, "\n\n") {
		var doc struct {
			Entry []reviewed.Entry `toml:"entry"`
		}
		name := reviewed.Entry{Path: ledgerTestA, Commit: ledgerCommitA}.File()
		if _, err := toml.Decode(part, &doc); err == nil && len(doc.Entry) == 1 {
			name = doc.Entry[0].File()
		}
		files[name] = part
	}
	return files
}

// withLedger is the PR's file list with the files of its ledger added.
func withLedger(files []prFile, ledger map[string]string) []prFile {
	for name := range ledger {
		files = append(files, prFile{Filename: name, Status: "added"})
	}
	slices.SortFunc(files[len(files)-len(ledger):], func(a, b prFile) int { return strings.Compare(a.Filename, b.Filename) })
	return files
}

// ledgerEnv is a main checkout with one Pi inventory entry and the record of divergence D12.
func ledgerEnv(t *testing.T) *ruleEnv {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{
		"docs/parity/PORT_MAP.md":        "| packages/coding-agent/src/core/agent.ts | x |\n",
		"docs/parity/divergences/D12.md": "# D12\n",
		"test/testdata-provenance.toml":  "[[tooling]]\nprefix = \"automation/ci/other/\"\nreason = \"r\"\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &ruleEnv{root: root}
}

// ledgerFiles is a PR that changes an expected value of each test file together with the code of its directory.
func ledgerFiles(tests ...string) []prFile {
	var files []prFile
	for _, name := range tests {
		dir := filepath.ToSlash(filepath.Dir(name))
		files = append(files,
			prFile{Filename: dir + "/code.go", Status: "modified", Patch: "@@ -1 +1 @@\n-x := 1\n+x := 2"},
			prFile{Filename: name, Status: "modified", Patch: "@@ -1 +1 @@\n-\twant := \"old value\"\n+\twant := \"new value\""})
	}
	return files
}

// changedBy says that each of the commits changed each of the files.
func changedBy(files []string, commits ...string) map[string][]string {
	out := map[string][]string{}
	for _, sha := range commits {
		out[sha] = files
	}
	return out
}

func TestRuleTestExpectationReviewedLedger(t *testing.T) {
	r, p := shippedRule(t, "test-expectation")
	env := ledgerEnv(t)
	nobehavior := ledgerRow(ledgerCommitA, ledgerTestA, "NOBEHAVIOR", "", "A PiG-only seam; no Pi behavior is involved.")
	for _, tc := range []struct {
		name    string
		ledger  string
		tests   []string
		commits []string
		result  string
		reason  string
		notices int
	}{
		{"no row and no trailer", "", []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "in the PR body and a commit trailer", 0},
		{"a row of the commit and the file", nobehavior, []string{ledgerTestA}, []string{ledgerCommitA}, resultPass, "admitted by reviewed ledger rows", 1},
		{"a row of another commit", ledgerRow(ledgerCommitB, ledgerTestA, "NOBEHAVIOR", "", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "in the PR body and a commit trailer", 0},
		{"a row of another file", ledgerRow(ledgerCommitA, ledgerTestB, "NOBEHAVIOR", "", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "in the PR body and a commit trailer", 0},
		{"a row with a prefix of nine characters", ledgerRow(ledgerCommitA[:9], ledgerTestA, "NOBEHAVIOR", "", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "in the PR body and a commit trailer", 0},
		{"a prefix that names two commits of the PR", ledgerRow(ledgerCommitA[:10], ledgerTestA, "NOBEHAVIOR", "", "r"), []string{ledgerTestA}, []string{ledgerCommitA, ledgerCommitA[:10] + strings.Repeat("c", 30)}, resultFail, "names 2 commits of the PR", 0},
		{"a ledger file that is not TOML", "[[entry]\npath = ", []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "reviewed ledger at the PR head cannot be read", 0},
		{"a ledger file that holds two rows", nobehavior + nobehavior, []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "holds 2 entries", 0},
		{"an empty ledger file", "\n", []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "holds 0 entries", 0},
		{"an unknown class", ledgerRow(ledgerCommitA, ledgerTestA, "WHATEVER", "", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, `unknown class "WHATEVER"`, 0},
		{"no reason", ledgerRow(ledgerCommitA, ledgerTestA, "NOBEHAVIOR", "", "  "), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "has no reason", 0},
		{"a CITE row of a Pi file main names", ledgerRow(ledgerCommitA, ledgerTestA, "CITE", "packages/coding-agent/src/core/agent.ts:9", "Pi changed it."), []string{ledgerTestA}, []string{ledgerCommitA}, resultPass, "admitted", 1},
		{"a CITE row of a Pi file main does not name", ledgerRow(ledgerCommitA, ledgerTestA, "CITE", "packages/x/src/invented.ts:9", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "does not name a Pi file", 0},
		{"a DIVERGENCE row of a record main has", ledgerRow(ledgerCommitA, ledgerTestA, "DIVERGENCE", "D12", "Diverges."), []string{ledgerTestA}, []string{ledgerCommitA}, resultPass, "admitted", 1},
		{"a DIVERGENCE row of a record nobody has", ledgerRow(ledgerCommitA, ledgerTestA, "DIVERGENCE", "D99", "r"), []string{ledgerTestA}, []string{ledgerCommitA}, resultFail, "docs/parity/divergences/D99.md", 0},
		{"two files, two rows", nobehavior + "\n" + ledgerRow(ledgerCommitB, ledgerTestB, "NOBEHAVIOR", "", "Also PiG-only."), []string{ledgerTestA, ledgerTestB}, []string{ledgerCommitA, ledgerCommitB}, resultPass, "2 changed test file(s)", 2},
		{"two files, one row", nobehavior, []string{ledgerTestA, ledgerTestB}, []string{ledgerCommitA, ledgerCommitB}, resultFail, ledgerTestB, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &data{Pull: pull{}, Files: ledgerFiles(tc.tests...), ReviewedLedger: ledgerOf(tc.ledger), CommitFiles: changedBy(tc.tests, tc.commits...)}
			for _, sha := range tc.commits {
				d.Commits = append(d.Commits, commit(sha, "a: b", true))
			}
			o := r.run(d, p, env)
			if o.result != tc.result || !strings.Contains(o.reason, tc.reason) || len(o.notices) != tc.notices {
				t.Fatalf("outcome %+v, want %s %q with %d notice(s)", o, tc.result, tc.reason, tc.notices)
			}
		})
	}
}

// A row for a divergence record that the PR itself adds stands; one the PR removes does not.
func TestRuleTestExpectationDivergenceRowOfARecordInThePR(t *testing.T) {
	r, p := shippedRule(t, "test-expectation")
	env := ledgerEnv(t)
	row := ledgerRow(ledgerCommitA, ledgerTestA, "DIVERGENCE", "D77", "Diverges.")
	for _, tc := range []struct {
		status string
		result string
	}{{"added", resultPass}, {"removed", resultFail}} {
		files := append(ledgerFiles(ledgerTestA), prFile{Filename: "docs/parity/divergences/D77.md", Status: tc.status})
		d := &data{Files: files, Commits: []prCommit{commit(ledgerCommitA, "a: b", true)}, ReviewedLedger: ledgerOf(row), CommitFiles: changedBy([]string{ledgerTestA}, ledgerCommitA)}
		if o := r.run(d, p, env); o.result != tc.result {
			t.Errorf("record %s by the PR: %+v, want %s", tc.status, o, tc.result)
		}
	}
}

// The row decides only the files it names: a file with no row still needs its citation, and the admitted one keeps its notice.
func TestRuleTestExpectationRowAndTrailerTogether(t *testing.T) {
	r, p := shippedRule(t, "test-expectation")
	env := ledgerEnv(t)
	cite := "Pi-Behavior-Change: packages/coding-agent/src/core/agent.ts:12"
	d := &data{
		Pull:           pull{Body: cite},
		Files:          ledgerFiles(ledgerTestA, ledgerTestB),
		Commits:        []prCommit{commit(ledgerCommitA, "a: b\n\n"+cite, true), commit(ledgerCommitB, "c: d", true)},
		ReviewedLedger: ledgerOf(ledgerRow(ledgerCommitA, ledgerTestA, "NOBEHAVIOR", "", "PiG-only.")),
		CommitFiles:    changedBy([]string{ledgerTestA}, ledgerCommitA),
	}
	if o := r.run(d, p, env); o.result != resultPass || len(o.notices) != 1 || !strings.Contains(o.notices[0], ledgerTestA) {
		t.Fatalf("the cited file and the admitted one: %+v", o)
	}
}

// The notice names the row, its class and its reason, and the whole check result stays a pass that asks for nothing.
func TestAdmittedExpectationIsPrintedAndAsksForNothing(t *testing.T) {
	cfg, err := loadConfig(configYAML)
	if err != nil {
		t.Fatal(err)
	}
	env := ledgerEnv(t)
	ledger := ledgerRow(ledgerCommitA, ledgerTestA, "NOBEHAVIOR", "", "The seam is PiG's own.") + "\n" +
		ledgerRow(ledgerCommitB, ledgerTestB, "CITE", "packages/coding-agent/src/core/agent.ts:9", "Pi changed this line.")
	d := &data{
		Repo: "o/r", PR: 7, Mode: modeFull, Relevant: []string{"test-expectation"},
		Files:          ledgerFiles(ledgerTestA, ledgerTestB),
		Commits:        []prCommit{commit(ledgerCommitA, "a: b", true), commit(ledgerCommitB, "c: d", true)},
		ReviewedLedger: ledgerOf(ledger),
		CommitFiles:    changedBy([]string{ledgerTestA, ledgerTestB}, ledgerCommitA, ledgerCommitB),
	}
	ev := evaluate(cfg, d, env)
	v := ev.Checks["test-expectation"]
	if v.Result != resultPass || ev.Result != resultPass || len(v.Warnings) != 0 || len(v.Notices) != 2 {
		t.Fatalf("verdict %+v, run %s", v, ev.Result)
	}
	var out bytes.Buffer
	report(&out, cfg, ev)
	for _, want := range []string{
		"NOTE  test-expectation",
		ledgerTestA + ": expected-value change admitted by reviewed ledger row aaaaaaaaaa, class NOBEHAVIOR: The seam is PiG's own.",
		ledgerTestB + ": expected-value change admitted by reviewed ledger row bbbbbbbbbb, class CITE packages/coding-agent/src/core/agent.ts:9: Pi changed this line.",
		"::notice title=pre-flight test-expectation #7::",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	if n := strings.Count(out.String(), "NOTE  test-expectation"); n != 2 {
		t.Errorf("%d notice line(s), want one per admitted file (2):\n%s", n, out.String())
	}
	for _, unwanted := range []string{"WARN ", "to acknowledge", "::warning", "::error"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("the output holds %q, an admitted file must ask for nothing:\n%s", unwanted, out.String())
		}
	}
	md := markdown(cfg, d, ev)
	if n := strings.Count(md, "📝"); n != 2 || strings.Contains(md, "acknowledge") {
		t.Errorf("the comment must hold two notice rows and no acknowledgement request:\n%s", md)
	}
}

// The ledger files of the PR are read at the PR's head commit, and only they: one call for each file that can admit a
// changed test file. A file the head does not have is no entry; any other failure leaves the check undecided.
func TestReviewedLedgerFetchedAtTheHeadCommit(t *testing.T) {
	cfg, err := loadConfig(configYAML)
	if err != nil {
		t.Fatal(err)
	}
	const oddTest = "a/odd name#1?x_test.go"
	row := ledgerRow(ledgerCommitA, ledgerTestA, "NOBEHAVIOR", "", "PiG-only.")
	oddRow := ledgerRow(ledgerCommitA, oddTest, "NOBEHAVIOR", "", "PiG-only.")
	entryA := reviewed.Entry{Path: ledgerTestA, Commit: ledgerCommitA}.File()
	for _, tc := range []struct {
		name   string
		test   string
		inPR   []prFile
		status int
		result string
		reason string
		failed bool
		ledger map[string]string
		asked  []string
	}{
		{"found", ledgerTestA, withLedger(nil, ledgerOf(row)), http.StatusOK, resultPass, "admitted", false, ledgerOf(row), []string{entryA}},
		{"a name the URL must escape", oddTest, withLedger(nil, ledgerOf(oddRow)), http.StatusOK, resultPass, "admitted", false, ledgerOf(oddRow), []string{reviewed.Entry{Path: oddTest, Commit: ledgerCommitA}.File()}},
		{"the PR holds no ledger file", ledgerTestA, nil, http.StatusOK, resultFail, "in the PR body and a commit trailer", false, nil, nil},
		{"the PR removes the file", ledgerTestA, []prFile{{Filename: entryA, Status: "removed"}}, http.StatusOK, resultFail, "in the PR body and a commit trailer", false, nil, nil},
		{"the file of another commit", ledgerTestA, withLedger(nil, ledgerOf(ledgerRow(ledgerCommitB, ledgerTestA, "NOBEHAVIOR", "", "r"))), http.StatusOK, resultFail, "in the PR body and a commit trailer", false, nil, nil},
		{"the file of another test file", ledgerTestA, withLedger(nil, ledgerOf(ledgerRow(ledgerCommitA, ledgerTestB, "NOBEHAVIOR", "", "r"))), http.StatusOK, resultFail, "in the PR body and a commit trailer", false, nil, nil},
		{"the file is not at the head", ledgerTestA, withLedger(nil, ledgerOf(row)), http.StatusNotFound, resultFail, "holds 0 entries", false, map[string]string{entryA: ""}, []string{entryA}},
		{"the API refuses", ledgerTestA, withLedger(nil, ledgerOf(row)), http.StatusForbidden, resultError, "source reviewed-ledger failed", true, nil, []string{entryA}},
		{"the API is down", ledgerTestA, withLedger(nil, ledgerOf(row)), http.StatusBadGateway, resultError, "source reviewed-ledger failed", true, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refs, asked []string
			files, err := json.Marshal(append(ledgerFiles(tc.test), tc.inPR...))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch p := r.URL.Path; {
				case p == "/repos/o/r/pulls/7":
					_, _ = fmt.Fprintf(w, `{"number":7,"body":"","mergeable":true,"commits":1,"changed_files":%d,"head":{"sha":"headsha"},"base":{"sha":"b","ref":"main"}}`, 2+len(tc.inPR))
				case p == "/repos/o/r/pulls/7/files":
					_, _ = w.Write(files)
				case p == "/repos/o/r/pulls/7/commits":
					_, _ = io.WriteString(w, `[{"sha":"`+ledgerCommitA+`"}]`)
				case strings.HasPrefix(p, "/repos/o/r/compare/"):
					_, _ = io.WriteString(w, `{"status":"ahead","ahead_by":1,"behind_by":0}`)
				case strings.HasPrefix(p, "/repos/o/r/commits/"):
					_, _ = io.WriteString(w, `{"files":[{"filename":`+strconv.Quote(tc.test)+`}]}`)
				case strings.HasPrefix(p, "/repos/o/r/contents/"+reviewed.Dir+"/"):
					name := strings.TrimPrefix(p, "/repos/o/r/contents/")
					refs = append(refs, r.URL.Query().Get("ref"))
					asked = append(asked, name)
					if tc.status != http.StatusOK {
						http.Error(w, "no", tc.status)
						return
					}
					_, _ = io.WriteString(w, `{"encoding":"base64","content":"`+base64.StdEncoding.EncodeToString([]byte(tc.ledger[name]))+`"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			d, err := fetchWith(t, cfg, fetchOptions{apiBase: server.URL, repo: "o/r", pr: 7, version: "v"}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if tc.asked != nil && !slices.Equal(slices.Compact(asked), tc.asked) || tc.asked == nil && !tc.failed && len(asked) != 0 {
				t.Fatalf("the files asked for are %q, want %q", asked, tc.asked)
			}
			if slices.ContainsFunc(refs, func(ref string) bool { return ref != "headsha" }) {
				t.Fatalf("the ledger was asked for at ref %q, want the head commit headsha", refs)
			}
			if _, failed := d.SourceErrors["reviewed-ledger"]; failed != tc.failed || !maps.Equal(d.ReviewedLedger, tc.ledger) {
				t.Fatalf("source errors %v, ledger %q, want %q", d.SourceErrors, d.ReviewedLedger, tc.ledger)
			}
			ev := evaluate(cfg, d, ledgerEnv(t))
			if v := ev.Checks["test-expectation"]; v.Result != tc.result || !strings.Contains(v.Reason, tc.reason) {
				t.Fatalf("test-expectation %s %q, want %s %q", v.Result, v.Reason, tc.result, tc.reason)
			}
		})
	}
}
