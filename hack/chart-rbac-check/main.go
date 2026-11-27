// Command chart-rbac-check fails when a rule in the generated manager
// ClusterRole is not granted by the ClusterRole shipped in the Helm chart.
//
// Usage: chart-rbac-check <generated role.yaml> <rendered chart clusterrole>
package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

type permission struct {
	group, resource, verb string
}

func (p permission) String() string {
	group := p.group
	if group == "" {
		group = `""`
	}
	return fmt.Sprintf("apiGroup=%s resource=%s verb=%s", group, p.resource, p.verb)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: chart-rbac-check <generated role.yaml> <rendered chart clusterrole>")
		os.Exit(2)
	}
	generated, err := readRules(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	chart, err := readRules(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	missing := missingPermissions(generated, chart)
	if len(missing) == 0 {
		fmt.Println("chart ClusterRole grants every generated RBAC rule")
		return
	}
	fmt.Fprintf(os.Stderr, "%s grants permissions that %s does not:\n", os.Args[1], os.Args[2])
	for _, p := range missing {
		fmt.Fprintf(os.Stderr, "  %s\n", p)
	}
	fmt.Fprintln(os.Stderr, "Add them to charts/kairos-operator/templates/clusterrole.yaml.")
	os.Exit(1)
}

// readRules returns the rules of every ClusterRole in a (multi-document) YAML file.
func readRules(path string) ([]rbacv1.PolicyRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []rbacv1.PolicyRule
	found := false
	for _, doc := range bytes.Split(data, []byte("\n---")) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		var role rbacv1.ClusterRole
		if err := yaml.Unmarshal(doc, &role); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if role.Kind != "ClusterRole" {
			continue
		}
		found = true
		rules = append(rules, role.Rules...)
	}
	if !found {
		return nil, fmt.Errorf("%s: no ClusterRole found", path)
	}
	return rules, nil
}

func expand(rules []rbacv1.PolicyRule) []permission {
	var out []permission
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					out = append(out, permission{g, res, v})
				}
			}
		}
	}
	return out
}

func matches(value, pattern string) bool {
	return pattern == rbacv1.APIGroupAll || pattern == value
}

func granted(p permission, by []permission) bool {
	for _, c := range by {
		if matches(p.group, c.group) && matches(p.resource, c.resource) && matches(p.verb, c.verb) {
			return true
		}
	}
	return false
}

func missingPermissions(generated, chart []rbacv1.PolicyRule) []permission {
	have := expand(chart)
	seen := map[permission]bool{}
	var missing []permission
	for _, p := range expand(generated) {
		if seen[p] || granted(p, have) {
			continue
		}
		seen[p] = true
		missing = append(missing, p)
	}
	sort.Slice(missing, func(i, j int) bool {
		return strings.Compare(missing[i].String(), missing[j].String()) < 0
	})
	return missing
}
