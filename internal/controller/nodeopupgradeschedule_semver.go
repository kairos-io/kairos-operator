package controller

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// semverImagePolicy selects semantic versions using a fixed constraint. Prereleases are
// excluded unless the constraint explicitly includes them.
type semverImagePolicy struct {
	constraint *semver.Constraints
}

// newSemverImagePolicy validates and parses a semantic version constraint.
func newSemverImagePolicy(expression string) (*semverImagePolicy, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("semver constraint must not be empty")
	}
	constraint, err := semver.NewConstraint(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid semver constraint %q: %w", expression, err)
	}
	return &semverImagePolicy{constraint: constraint}, nil
}

// selectUpgrade returns the original tag of the highest matching version newer
// than currentTag, or an empty string if no upgrade is available. Tags must be
// full semantic versions, optionally prefixed with a lowercase v. Invalid
// candidate tags are ignored; an invalid currentTag is an error because version
// ordering cannot be established. Full image references are not accepted.
//
// Equal versions are resolved by choosing the lexicographically smallest tag,
// so registry listing order cannot change the selection. Build metadata does
// not affect version precedence and cannot make a version an upgrade.
func (p *semverImagePolicy) selectUpgrade(tags []string, currentTag string) (string, error) {
	current, err := parseSemverTag(currentTag)
	if err != nil {
		return "", fmt.Errorf("invalid current version tag %q: %w", currentTag, err)
	}

	var highest *semver.Version
	var selected string
	for _, tag := range tags {
		version, err := parseSemverTag(tag)
		if err != nil || !p.constraint.Check(version) || !version.GreaterThan(current) {
			continue
		}
		if highest == nil || version.GreaterThan(highest) || (version.Equal(highest) && tag < selected) {
			highest = version
			selected = tag
		}
	}
	return selected, nil
}

func parseSemverTag(tag string) (*semver.Version, error) {
	return semver.StrictNewVersion(strings.TrimPrefix(tag, "v"))
}
