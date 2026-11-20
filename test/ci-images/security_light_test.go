// SPDX-License-Identifier: MIT

package ciimages

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/MichaelKinsy/PiG/internal/testenv"
)

// planStepOf returns the pull_request types of a workflow and the plan step of its changes job.
func planStepOf(t *testing.T, name string) (types []string, env map[string]string, run string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		On struct {
			PullRequest struct{ Types []string } `yaml:"pull_request"`
		}
		Jobs map[string]struct {
			Steps []struct {
				ID  string
				Env map[string]string
				Run string
			}
		}
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, step := range raw.Jobs["changes"].Steps {
		if step.ID == "plan" {
			return raw.On.PullRequest.Types, step.Env, step.Run
		}
	}
	t.Fatalf("%s: the changes job has no plan step", name)
	return nil, nil, ""
}

// Security result and CI result are both required, and in label mode each fails on a light run. They agree only when both
// workflows start on the same pull request events and give the planner the same inputs.
func TestSecurityWorkflowPlansTheSameRunAsCI(t *testing.T) {
	ciTypes, ciEnv, _ := planStepOf(t, "ci.yml")
	types, env, run := planStepOf(t, "security.yml")
	if !slices.Equal(types, ciTypes) {
		t.Errorf("security.yml runs on pull_request %v, ci.yml on %v: a label or a review event would start the full run of one workflow only", types, ciTypes)
	}
	for _, name := range []string{"EVENT", "MODE", "FULL_REQUESTED"} {
		if env[name] == "" || env[name] != ciEnv[name] {
			t.Errorf("security.yml plans with %s = %q, ci.yml with %q", name, env[name], ciEnv[name])
		}
	}

	// The step's own command line, on the committed tier table.
	plan := func(event, mode, requested string) string {
		t.Helper()
		output := filepath.Join(t.TempDir(), "output")
		cmd := exec.CommandContext(t.Context(), testenv.Bash(t), "-eo", "pipefail", "-c", run)
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "EVENT="+event, "MODE="+mode, "FULL_REQUESTED="+requested, "GITHUB_OUTPUT="+output)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("plan step for %s %q %q: %v\n%s", event, mode, requested, err, out)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, tc := range []struct{ event, mode, requested, want string }{
		{"pull_request", "label", "false", "full=false\nfull-required=true\n"},
		{"pull_request", "label", "true", "full=true\nfull-required=true\n"},
		{"pull_request", "queue", "false", "full=false\nfull-required=false\n"},
		{"pull_request", "", "false", "full=true\nfull-required=false\n"},
		{"merge_group", "label", "false", "full=true\nfull-required=false\n"},
		{"push", "label", "false", "full=true\nfull-required=false\n"},
		{"schedule", "queue", "false", "full=true\nfull-required=false\n"},
	} {
		if got := plan(tc.event, tc.mode, tc.requested); got != tc.want {
			t.Errorf("%s in mode %q, full requested %s: outputs %q, want %q", tc.event, tc.mode, tc.requested, got, tc.want)
		}
	}
	// A planner that fails must fail the step: an empty plan would read as "not the full run".
	cmd := exec.CommandContext(t.Context(), testenv.Bash(t), "-eo", "pipefail", "-c", run)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "EVENT=pull_request", "MODE=nonsense", "FULL_REQUESTED=false", "GITHUB_OUTPUT="+filepath.Join(t.TempDir(), "output"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("the plan step succeeded for an unknown PIG_PR_CI_LIGHT value:\n%s", out)
	}
}

// The light run leaves out the two jobs that take most of the workflow's time and nothing else: the secret scans, the
// dependency review and the analysis of the interpreted SDKs run on every pull request push.
func TestLightSecurityRunLeavesOnlySourceAssuranceAndGoCodeQLToTheFullRun(t *testing.T) {
	workflow := readWorkflow(t, "security.yml")
	const full = "needs.changes.outputs.full == 'true'"
	fullOnly := []string{"source-assurance", "codeql-go"}
	for id, job := range workflow.Jobs {
		if id == "changes" || id == "result" {
			continue
		}
		if got, want := strings.Contains(job.If, full), slices.Contains(fullOnly, id); got != want {
			t.Errorf("security.yml job %s has if: %q; runs in the full run only = %v, want %v", id, job.If, got, want)
		}
		if strings.Contains(job.If, "outputs.full") && !slices.Contains(needsOf(job.Needs), "changes") {
			t.Errorf("security.yml job %s reads the plan but does not need the changes job", id)
		}
	}
	for _, id := range fullOnly {
		job, ok := workflow.Jobs[id]
		if !ok {
			t.Fatalf("security.yml has no %s job", id)
		}
		if !strings.Contains(job.If, "needs.changes.outputs.product == 'true'") {
			t.Errorf("security.yml job %s has if: %q, want it skipped for a docs-only change as before", id, job.If)
		}
	}
}

// securityResultRun runs the shell of the Security result job with each job's result and the plan, as a merge group by default.
func securityResultRun(t *testing.T, set map[string]string) (string, error) {
	t.Helper()
	script := ""
	var declared map[string]string
	for _, step := range readWorkflow(t, "security.yml").Jobs["result"].Steps {
		if strings.Contains(step.Run, "expect()") {
			script, declared = step.Run, step.Env
		}
	}
	if script == "" {
		t.Fatal("the Security result job has no aggregation step")
	}
	values := map[string]string{
		"EVENT": "merge_group", "PRIVATE": "false", "PRODUCT": "true", "FULL": "true", "FULL_REQUIRED": "false",
		"CHANGES": "success", "SOURCE": "success", "GITLEAKS": "success", "TRUFFLEHOG": "success",
		"REVIEW": "skipped", "CODEQL": "success", "CODEQL_GO": "success",
	}
	for name := range declared {
		if _, ok := values[name]; !ok {
			t.Fatalf("the Security result step reads %s, which this test does not set", name)
		}
	}
	for name := range values {
		if _, ok := declared[name]; !ok {
			t.Fatalf("this test sets %s, which the Security result step no longer reads", name)
		}
	}
	env := os.Environ()
	for name, value := range merge(values, set) {
		if _, ok := values[name]; !ok {
			t.Fatalf("the test case sets %s, which the Security result step does not read", name)
		}
		env = append(env, name+"="+value)
	}
	cmd := exec.CommandContext(t.Context(), testenv.Bash(t), "-euo", "pipefail", "-c", script)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestSecurityResultRequiresEveryPlannedJob(t *testing.T) {
	pullRequest := map[string]string{"EVENT": "pull_request", "REVIEW": "success"}
	light := merge(pullRequest, map[string]string{"FULL": "false", "SOURCE": "skipped", "CODEQL_GO": "skipped"})
	label := map[string]string{"FULL_REQUIRED": "true"}
	docsOnly := map[string]string{"PRODUCT": "false", "SOURCE": "skipped", "REVIEW": "skipped", "CODEQL": "skipped", "CODEQL_GO": "skipped"}
	for _, tc := range []struct {
		name string
		set  map[string]string
		pass bool
	}{
		{"merge group with every job", nil, true},
		{"merge group that skipped Source assurance", map[string]string{"SOURCE": "skipped"}, false},
		{"merge group that skipped CodeQL (go)", map[string]string{"CODEQL_GO": "skipped"}, false},
		{"merge group that planned the light run", map[string]string{"FULL": "false", "SOURCE": "skipped", "CODEQL_GO": "skipped"}, false},
		{"merge group without a plan", map[string]string{"FULL": "", "SOURCE": "skipped", "CODEQL_GO": "skipped"}, false},
		{"push to main with every job", map[string]string{"EVENT": "push"}, true},
		{"push to main that skipped Source assurance", map[string]string{"EVENT": "push", "SOURCE": "skipped"}, false},
		{"nightly run that planned the light run", map[string]string{"EVENT": "schedule", "FULL": "false", "SOURCE": "skipped", "CODEQL_GO": "skipped"}, false},
		{"full pull request", pullRequest, true},
		{"full pull request that skipped Source assurance", merge(pullRequest, map[string]string{"SOURCE": "skipped"}), false},
		{"full pull request that skipped CodeQL (go)", merge(pullRequest, map[string]string{"CODEQL_GO": "skipped"}), false},
		{"full pull request whose CodeQL (go) failed", merge(pullRequest, map[string]string{"CODEQL_GO": "failure"}), false},
		{"pull request without a plan", merge(light, map[string]string{"FULL": ""}), false},
		{"light pull request behind a merge queue", light, true},
		{"light pull request whose Gitleaks failed", merge(light, map[string]string{"GITLEAKS": "failure"}), false},
		{"light pull request whose TruffleHog was skipped", merge(light, map[string]string{"TRUFFLEHOG": "skipped"}), false},
		{"light pull request that skipped the dependency review", merge(light, map[string]string{"REVIEW": "skipped"}), false},
		{"light pull request that skipped the interpreted CodeQL", merge(light, map[string]string{"CODEQL": "skipped"}), false},
		{"light pull request whose planned-away Source assurance ran and failed", merge(light, map[string]string{"SOURCE": "failure"}), false},
		{"light pull request without a merge queue", merge(light, label), false},
		{"full pull request without a merge queue", merge(pullRequest, label), true},
		{"full pull request without a merge queue and a failed job", merge(pullRequest, label, map[string]string{"SOURCE": "failure"}), false},
		{"docs-only light pull request without a merge queue", merge(light, label, docsOnly), true},
		{"docs-only merge group", docsOnly, true},
		{"private repository", map[string]string{"PRIVATE": "true", "CODEQL": "skipped", "CODEQL_GO": "skipped"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := securityResultRun(t, tc.set)
			if (err == nil) != tc.pass {
				t.Fatalf("pass = %v, want %v\n%s", err == nil, tc.pass, output)
			}
		})
	}
	output, _ := securityResultRun(t, merge(light, label))
	if !strings.Contains(output, "full run required: add the ci:full label") {
		t.Errorf("the failure of a light run without a merge queue does not say how to get the full run:\n%s", output)
	}
}
