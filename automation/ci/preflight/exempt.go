// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed exempt.yaml
var exemptYAML []byte

// An exemption names checks that do not apply to the pull requests it covers: each passes with the exemption's reason
// and is not decided. What it covers by is not the pull request author's to choose: the account that opened the pull
// request, and a branch of the repository itself, which only a writer of the repository can push.
type exemption struct {
	Name   string   `yaml:"name" json:"name"`
	Author string   `yaml:"author" json:"author"`
	Head   string   `yaml:"head" json:"head"`
	Checks []string `yaml:"checks" json:"checks"`
	Reason string   `yaml:"reason" json:"reason"`

	head  *regexp.Regexp
	input string
}

// loadShipped is the configuration the program is built with: config.yaml and the exemptions of exempt.yaml.
func loadShipped() (config, error) {
	cfg, err := loadConfig(configYAML)
	if err != nil {
		return config{}, err
	}
	if cfg.Exempt, err = loadExemptions(exemptYAML, cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func loadExemptions(data []byte, cfg config) ([]exemption, error) {
	var file struct {
		Exempt []exemption `yaml:"exempt"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("exempt: %w", err)
	}
	seen := map[string]bool{}
	for i := range file.Exempt {
		x := &file.Exempt[i]
		if x.Name == "" {
			return nil, fmt.Errorf("exempt: entry %d has no name", i+1)
		}
		if seen[x.Name] {
			return nil, fmt.Errorf("exempt: entry %q is declared twice", x.Name)
		}
		seen[x.Name] = true
		if err := x.validate(cfg); err != nil {
			return nil, fmt.Errorf("exempt: entry %q: %w", x.Name, err)
		}
	}
	return file.Exempt, nil
}

func (x *exemption) validate(cfg config) error {
	switch {
	case x.Author == "":
		return errors.New("needs the author whose pull requests it covers")
	case len(x.Checks) == 0:
		return errors.New("names no check")
	case x.Reason == "":
		return errors.New("needs a reason")
	}
	for _, name := range x.Checks {
		if !slices.ContainsFunc(cfg.Checks, func(c check) bool { return c.Name == name }) {
			return fmt.Errorf("names check %q, which config.yaml does not declare", name)
		}
	}
	re, err := globRegexp(x.Head)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	x.head = re
	raw, err := json.Marshal(x)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte("exempt\x00"), raw...))
	x.input = hex.EncodeToString(sum[:])
	return nil
}

// covers reports whether the exemption is for this pull request: its author opened it, from a branch of the repository
// itself that the head pattern matches. A pull request from a fork names the fork as its head repository, whatever its
// branch is called, and one whose fork is gone names none.
func (x exemption) covers(d *data) bool {
	head := d.Pull.Head
	return d.Pull.User.Login == x.Author && head.Repo.FullName != "" && strings.EqualFold(head.Repo.FullName, d.Repo) && x.head.MatchString(head.Ref)
}

// exemptVerdict is the verdict of a check that an exemption takes from this pull request. Its input is the exemption,
// never the check's own input: a verdict stored while the check was exempt is not reused once the check is decided
// again, nor the reverse.
func (cfg config) exemptVerdict(d *data, name string) (verdict, bool) {
	for _, x := range cfg.Exempt {
		if slices.Contains(x.Checks, name) && x.covers(d) {
			return verdict{Input: x.input, Result: resultPass, Reason: clip(fmt.Sprintf("exempt (%s): %s", x.Name, x.Reason), maxReason)}, true
		}
	}
	return verdict{}, false
}

// bindExemptions makes the exemptions part of the run version: a changed exempt.yaml decides every pull request again
// instead of reusing its previous verdict.
func bindExemptions(run io.Writer, exempt []exemption) {
	for _, x := range exempt {
		_, _ = fmt.Fprintf(run, "exempt %s\x00", x.input)
	}
}
