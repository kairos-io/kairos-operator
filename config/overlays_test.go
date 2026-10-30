package config_test

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type selectorDoc struct {
	Spec struct {
		Selector    *selector `json:"selector"`
		PodSelector *selector `json:"podSelector"`
		Template    struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"template"`
	} `json:"spec"`
}

type selector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

func load(t *testing.T, path string) selectorDoc {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d selectorDoc
	for _, part := range strings.Split(string(b), "\n---\n") {
		var cur selectorDoc
		if err := yaml.Unmarshal([]byte(part), &cur); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(cur.Spec.Template.Metadata.Labels) > 0 || cur.Spec.Selector != nil || cur.Spec.PodSelector != nil {
			d = cur
		}
	}
	return d
}

// The optional prometheus and network-policy overlays must select the labels
// the manager pod (and so the metrics Service) really carries.
func TestMetricsOverlaysSelectTheManager(t *testing.T) {
	podLabels := load(t, "manager/manager.yaml").Spec.Template.Metadata.Labels
	if len(podLabels) == 0 {
		t.Fatal("manager pod template has no labels")
	}

	cases := map[string]*selector{
		"prometheus/monitor.yaml":                   load(t, "prometheus/monitor.yaml").Spec.Selector,
		"network-policy/allow-metrics-traffic.yaml": load(t, "network-policy/allow-metrics-traffic.yaml").Spec.PodSelector,
	}
	for name, sel := range cases {
		if sel == nil {
			t.Fatalf("%s: no selector", name)
		}
		for k, v := range sel.MatchLabels {
			if podLabels[k] != v {
				t.Errorf("%s selects %s=%s but the manager pod has %s=%q", name, k, v, k, podLabels[k])
			}
		}
		if len(sel.MatchLabels) == 0 {
			t.Errorf("%s: empty matchLabels", name)
		}
	}
}
