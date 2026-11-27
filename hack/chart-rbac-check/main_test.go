package main

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func rule(groups, resources, verbs []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: groups, Resources: resources, Verbs: verbs}
}

func TestMissingPermissions(t *testing.T) {
	chart := []rbacv1.PolicyRule{
		rule([]string{""}, []string{"pods", "secrets"}, []string{"get", "list"}),
		rule([]string{"authentication.k8s.io"}, []string{"tokenreviews"}, []string{"create"}),
		rule([]string{"apps"}, []string{"*"}, []string{"get"}),
	}

	t.Run("subset passes and chart-only rules are allowed", func(t *testing.T) {
		generated := []rbacv1.PolicyRule{
			rule([]string{""}, []string{"pods"}, []string{"get", "list"}),
			rule([]string{"apps"}, []string{"daemonsets"}, []string{"get"}),
		}
		if got := missingPermissions(generated, chart); len(got) != 0 {
			t.Fatalf("expected nothing missing, got %v", got)
		}
	})

	t.Run("missing resource and verb are reported", func(t *testing.T) {
		generated := []rbacv1.PolicyRule{
			rule([]string{"coordination.k8s.io"}, []string{"leases"}, []string{"get"}),
			rule([]string{""}, []string{"pods"}, []string{"get", "delete"}),
		}
		got := missingPermissions(generated, chart)
		want := []permission{
			{"", "pods", "delete"},
			{"coordination.k8s.io", "leases", "get"},
		}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})
}
