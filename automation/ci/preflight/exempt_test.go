// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	forkRepo = "Patrick-Kappen/PiG"
	syncApp  = "patrick-kappen-release[bot]"
)

// The checks that judge a contribution by the description and the own commits of its pull request, in config.yaml's
// order. syncPull fails every one of them when nothing is exempt.
var contributionChecks = []string{"template", "signoff", "depends-on", "bug-ledger", "changelog", "test-expectation"}

func shipped(t *testing.T) config {
	t.Helper()
	cfg, err := loadShipped()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func withoutExemptions(cfg config) config {
	cfg.Exempt = nil
	return cfg
}

func decode(t *testing.T, text string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), into); err != nil {
		t.Fatal(err)
	}
}

func syncPullJSON(author string, commits, files int) string {
	raw, err := json.Marshal(map[string]any{
		"number": 42, "state": "open", "mergeable": true, "commits": commits, "changed_files": files,
		"body": "Brings MichaelKinsy/PiG `main` into `main`.\n\n## Commits\n\n- fix(interactive): an extension's ctx.abort() returns (#480)",
		"user": map[string]string{"login": author},
		"head": map[string]any{"sha": "de9b7bf310", "ref": "sync/upstream", "repo": map[string]string{"full_name": forkRepo}},
		"base": map[string]string{"sha": "50632951c0", "ref": "main"},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// syncPull is a pull request as the fork sync opens it (.github/fork/sync.sh), with every check relevant: a description
// without the template, a squash merge of upstream that carries no sign-off, names the commits it replaced in trailers
// only, is titled as a fix and leaves the bug ledger alone, the merge by GitHub's merge API whose second parent is not on
// the base, a changelog fragment named after a replaced commit, and an expected value edited next to its code with no
// citation in the description.
func syncPull(t *testing.T, cfg config) *data {
	t.Helper()
	d := &data{Repo: forkRepo, Version: "v", Mode: modeFull}
	decode(t, syncPullJSON(syncApp, 2, 3), &d.Pull)
	decode(t, `[
		{"sha":"a4c21e4830","parents":[{"sha":"2e6b133e60"}],"commit":{
			"message":"fix(interactive): an extension's ctx.abort() returns (#480)\n\nFix-Commit: a14dd43d7bb1a952c556b3ec891c397e8b9e32bb",
			"committer":{"name":"GitHub","email":"noreply@github.com"},"verification":{"verified":true,"reason":"valid"}}},
		{"sha":"de9b7bf310","parents":[{"sha":"50632951c0"},{"sha":"a4c21e4830"}],"merge":{"on_base":false},"commit":{
			"message":"Merge MichaelKinsy/PiG main a4c21e483 into main",
			"committer":{"name":"GitHub","email":"noreply@github.com"},"verification":{"verified":true,"reason":"valid"}}}
	]`, &d.Commits)
	d.Files = []prFile{
		{Filename: "changelog.d/ext-abort-hang-a14dd43d7b.md", Status: "added", SHA: "b1", Patch: "@@ -0,0 +1,3 @@\n+### Fixed\n+\n+- An abort returns. <!-- fix: a14dd43d7b -->"},
		{Filename: "coding/abort.go", Status: "modified", SHA: "b2", Patch: "@@ -1 +1,2 @@\n package coding\n+var aborted bool"},
		{Filename: "coding/abort_test.go", Status: "modified", SHA: "b3", Patch: "@@ -3 +3 @@\n-\twant := \"old reply\"\n+\twant := \"new reply\""},
	}
	for _, c := range cfg.Checks {
		d.Relevant = append(d.Relevant, c.Name)
	}
	return d
}

func failedChecks(cfg config, ev evaluation) []string {
	var names []string
	for _, c := range cfg.Checks {
		if r := ev.Checks[c.Name].Result; r == resultFail || r == resultError {
			names = append(names, c.Name)
		}
	}
	return names
}

func TestAForkSyncPassesTheChecksOfAContribution(t *testing.T) {
	cfg := shipped(t)
	env := &ruleEnv{root: t.TempDir()}
	d := syncPull(t, cfg)
	if got := failedChecks(cfg, evaluate(withoutExemptions(cfg), d, env)); !slices.Equal(got, contributionChecks) {
		t.Fatalf("with nothing exempt the sync fails %v, want %v", got, contributionChecks)
	}
	ev := evaluate(cfg, d, env)
	if got := failedChecks(cfg, ev); len(got) != 0 {
		t.Fatalf("the sync fails %v: %+v", got, ev.Checks)
	}
	for _, name := range contributionChecks {
		if v := ev.Checks[name]; v.Result != resultPass || v.Reason != "exempt (fork-sync): its commits were judged on the pull requests that made them" {
			t.Errorf("%s: %+v, want a pass that names the exemption", name, v)
		}
	}
	if ev.Result != resultScan || ev.cheapFailed(cfg) {
		t.Fatalf("verdict %q: the scans of the sync's content must come next", ev.Result)
	}
	for _, branch := range []string{"sync/performance", "sync/upstream"} {
		d.Pull.Head.Ref = branch
		if got := failedChecks(cfg, evaluate(cfg, d, env)); len(got) != 0 {
			t.Errorf("the sync from %s fails %v", branch, got)
		}
	}
}

// What a sync brings is still checked: only the checks of a contribution's description and own commits are exempt.
func TestAForkSyncKeepsTheChecksOfItsContent(t *testing.T) {
	cfg := shipped(t)
	for want, change := range map[string]func(d *data){
		"signed":     func(d *data) { d.Commits[0].Commit.Verification.Verified = false },
		"up-to-date": func(d *data) { d.CompareMain.BehindBy = 2 },
		// REUSE-IgnoreStart
		"licenses": func(d *data) {
			d.Files = append(d.Files, prFile{Filename: "x/gpl.go", Status: "added", Patch: "@@ -0,0 +1 @@\n+// SPDX-License-Identifier: GPL-3.0-only"})
		},
		// REUSE-IgnoreEnd
	} {
		d := syncPull(t, cfg)
		change(d)
		if got := failedChecks(cfg, evaluate(cfg, d, &ruleEnv{root: t.TempDir()})); !slices.Equal(got, []string{want}) {
			t.Errorf("the sync fails %v, want only %s", got, want)
		}
	}
}

func TestOnlyASyncOfTheRepositoryItselfIsExempt(t *testing.T) {
	cfg := shipped(t)
	env := &ruleEnv{root: t.TempDir()}
	for name, change := range map[string]func(d *data){
		"another author":           func(d *data) { d.Pull.User.Login = "someone" },
		"a branch of a fork":       func(d *data) { d.Pull.Head.Repo.FullName = "someone/PiG" },
		"a fork that is gone":      func(d *data) { d.Pull.Head.Repo.FullName = "" },
		"a branch that is no sync": func(d *data) { d.Pull.Head.Ref = "team/patrick/topic" },
		"a sync in another name":   func(d *data) { d.Pull.Head.Ref = "team/sync/upstream" },
		"a name that starts alike": func(d *data) { d.Pull.Head.Ref = "syncing/upstream" },
		"the prefix alone":         func(d *data) { d.Pull.Head.Ref = "sync" },
		"a branch below a sync":    func(d *data) { d.Pull.Head.Ref = "sync/upstream/topic" },
	} {
		d := syncPull(t, cfg)
		change(d)
		if got := failedChecks(cfg, evaluate(cfg, d, env)); !slices.Equal(got, contributionChecks) {
			t.Errorf("%s fails %v, want %v", name, got, contributionChecks)
		}
	}
	// GitHub does not tell a repository's name apart by case.
	d := syncPull(t, cfg)
	d.Repo = strings.ToLower(forkRepo)
	if got := failedChecks(cfg, evaluate(cfg, d, env)); len(got) != 0 {
		t.Errorf("the repository named in another case fails %v", got)
	}
}

func TestAnExemptVerdictIsNotAStoredOne(t *testing.T) {
	cfg := shipped(t)
	env := &ruleEnv{root: t.TempDir()}
	d := syncPull(t, cfg)
	decided := evaluate(withoutExemptions(cfg), d, env)
	exempt := evaluate(cfg, d, env)
	d.Previous = &state{Engine: cfg.EngineRevision, Checks: decided.Checks}
	if got := failedChecks(cfg, evaluate(cfg, d, env)); len(got) != 0 {
		t.Errorf("an exempt check took the failure stored before it was exempt: %v", got)
	}
	d.Previous = &state{Engine: cfg.EngineRevision, Checks: exempt.Checks}
	if got := failedChecks(cfg, evaluate(withoutExemptions(cfg), d, env)); !slices.Equal(got, contributionChecks) {
		t.Errorf("a check that is decided again took the pass stored while it was exempt: fails %v, want %v", got, contributionChecks)
	}
}

func TestATooLargeForkSyncFailsEveryCheck(t *testing.T) {
	cfg := shipped(t)
	d := syncPull(t, cfg)
	d.Limit = "300 commits, more than the 250 the API lists"
	ev := evaluate(cfg, d, &ruleEnv{root: t.TempDir()})
	for _, name := range contributionChecks {
		if v := ev.Checks[name]; v.Result != resultFail || !strings.Contains(v.Reason, "too large") {
			t.Errorf("%s: %+v, want the failure of a PR too large to judge", name, v)
		}
	}
}

func TestAnExemptCheckNeedsNoSource(t *testing.T) {
	cfg := shipped(t)
	d := syncPull(t, cfg)
	d.SourceErrors = map[string]string{"commits": "HTTP 502", "files": "HTTP 502", "depends-on": "HTTP 502"}
	ev := evaluate(cfg, d, &ruleEnv{root: t.TempDir()})
	for _, name := range contributionChecks {
		if v := ev.Checks[name]; v.Result != resultPass {
			t.Errorf("%s: %+v, want the exempt pass", name, v)
		}
	}
	if v := ev.Checks["signed"]; v.Result != resultError {
		t.Errorf("signed: %+v, want the error of its failed source", v)
	}
}

func TestTheExemptionsAreBoundIntoTheRunVersion(t *testing.T) {
	root := t.TempDir()
	version := func(exempt string) string {
		t.Helper()
		cfg, err := loadConfig(configYAML)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Exempt, err = loadExemptions([]byte(exempt), cfg); err != nil {
			t.Fatal(err)
		}
		v, err := bindMain(&cfg, root)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	const one = "exempt: [{name: a, author: bot, head: 'sync/*', checks: [template], reason: r}]"
	with := version(one)
	if with == version("exempt: []") {
		t.Fatal("an exemption left the run version as it was: a PR judged without it keeps its verdict")
	}
	if with != version(one) {
		t.Fatal("the same exemptions gave two run versions: no verdict is ever reused")
	}
	for field, edit := range map[string][2]string{
		"name":   {"name: a", "name: b"},
		"author": {"author: bot", "author: other"},
		"head":   {"'sync/*'", "'sync/**'"},
		"checks": {"[template]", "[template, signoff]"},
		"reason": {"reason: r", "reason: q"},
	} {
		if with == version(strings.Replace(one, edit[0], edit[1], 1)) {
			t.Errorf("an edited %s left the run version as it was", field)
		}
	}
	two := strings.TrimSuffix(one, "]") + ", {name: b, author: bot, head: 'x/*', checks: [signed], reason: r}]"
	if with == version(two) {
		t.Error("a second exemption left the run version as it was")
	}
}

func TestLoadExemptions(t *testing.T) {
	cfg, err := loadConfig(configYAML)
	if err != nil {
		t.Fatal(err)
	}
	const ok = "{name: a, author: bot, head: 'sync/*', checks: [template], reason: r}"
	for text, want := range map[string]int{"exempt: []": 0, "exempt: [" + ok + "]": 1} {
		if got, err := loadExemptions([]byte(text), cfg); err != nil || len(got) != want {
			t.Errorf("%s: %d exemption(s), error %v; want %d", text, len(got), err, want)
		}
	}
	for _, tc := range []struct{ name, yaml, want string }{
		{"not a list", "exempt: {name: a}", "exempt: yaml:"},
		{"unknown key", "exempts: []", "field exempts not found"},
		{"unknown field", "exempt: [{name: a, author: bot, head: 'sync/*', checks: [template], reason: r, label: sync}]", "field label not found"},
		{"no name", "exempt: [{author: bot, head: 'sync/*', checks: [template], reason: r}]", "entry 1 has no name"},
		{"the nameless entry is named by its place", "exempt: [" + ok + ", {author: bot, head: 'sync/*', checks: [template], reason: r}]", "entry 2 has no name"},
		{"duplicate", "exempt: [" + ok + ", " + ok + "]", `entry "a" is declared twice`},
		{"no author", "exempt: [{name: a, head: 'sync/*', checks: [template], reason: r}]", `entry "a": needs the author`},
		{"no head", "exempt: [{name: a, author: bot, checks: [template], reason: r}]", `entry "a": head: path pattern ""`},
		{"absolute head", "exempt: [{name: a, author: bot, head: /sync, checks: [template], reason: r}]", `head: path pattern "/sync" must be relative`},
		{"no check", "exempt: [{name: a, author: bot, head: 'sync/*', reason: r}]", `entry "a": names no check`},
		{"unknown check", "exempt: [{name: a, author: bot, head: 'sync/*', checks: [template, nope], reason: r}]", `names check "nope", which config.yaml does not declare`},
		{"no reason", "exempt: [{name: a, author: bot, head: 'sync/*', checks: [template]}]", `entry "a": needs a reason`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadExemptions([]byte(tc.yaml), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// forkSyncAPI answers as GitHub does for pull request 42 of the fork, a sync that author opened. Its one commit is
// unsigned, so the verdict fails on a cheap check and no scan starts.
func forkSyncAPI(author string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.TrimPrefix(r.URL.Path, "/repos/"+forkRepo); {
		case p == "/pulls/42":
			_, _ = io.WriteString(w, syncPullJSON(author, 1, 1))
		case p == "/pulls/42/files":
			_, _ = io.WriteString(w, `[{"filename":"README.md","status":"modified","sha":"b1","patch":"@@ -1 +1,2 @@\n x\n+y"}]`)
		case p == "/pulls/42/commits":
			_, _ = io.WriteString(w, `[{"sha":"a4c21e4830","parents":[{"sha":"2e6b133e60"}],"commit":{"message":"docs: x (#480)","verification":{"verified":false,"reason":"unsigned"}}}]`)
		case p == "/issues/42/comments":
			_, _ = io.WriteString(w, `[]`)
		case p == "/git/ref/heads/main":
			_, _ = io.WriteString(w, `{"object":{"sha":"50632951c0"}}`)
		case strings.HasPrefix(p, "/compare/"):
			_, _ = io.WriteString(w, `{"status":"ahead","ahead_by":1,"behind_by":0}`)
		default:
			http.NotFound(w, r)
		}
	})
}

// The program itself: the pull as the API sends it, and the exemptions the binary is built with.
func TestRunExemptsAForkSync(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "t")
	dir := t.TempDir()
	judge := func(author string) string {
		t.Helper()
		server := httptest.NewServer(forkSyncAPI(author))
		defer server.Close()
		var out, errOut strings.Builder
		if code := run([]string{"run", "--repo", forkRepo, "--api", server.URL, "--root", dir, "--bin", dir, "--work", dir, "--dry-run", "--prs", "42"}, &out, &errOut); code != 0 {
			t.Fatalf("exit %d:\n%s%s", code, out.String(), errOut.String())
		}
		return out.String()
	}
	exempt := regexp.MustCompile(`(?m)^PASS\s+template\s+exempt \(fork-sync\): `)
	if log := judge(syncApp); !exempt.MatchString(log) || !regexp.MustCompile(`(?m)^FAIL\s+signed\s`).MatchString(log) {
		t.Fatalf("want the sync's template exempt and its unsigned commit refused:\n%s", log)
	}
	if log := judge("someone"); exempt.MatchString(log) || !regexp.MustCompile(`(?m)^FAIL\s+template\s`).MatchString(log) {
		t.Fatalf("want the template of another author's pull request decided:\n%s", log)
	}
}
