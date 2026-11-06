package config_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type managerDeployment struct {
	Kind string `json:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
					Env   []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func kustomizeBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("KUSTOMIZE"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	bin, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize not found; set KUSTOMIZE or run through make test")
	}
	return bin
}

func kustomize(t *testing.T, bin, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("kustomize %s in %s: %v\n%s", strings.Join(args, " "), dir, err, stderr)
	}
	return out
}

// copyConfig gives each case its own copy of config/, since the make targets
// run `kustomize edit` and rewrite kustomization files in place.
func copyConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "config"), os.DirFS(".")); err != nil {
		t.Fatal(err)
	}
	return root
}

// managerImages returns the manager container image and its OPERATOR_IMAGE.
func managerImages(t *testing.T, build []byte) (image, operatorImage string) {
	t.Helper()
	for _, doc := range strings.Split(string(build), "\n---\n") {
		var d managerDeployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		if d.Kind != "Deployment" {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != "manager" {
				continue
			}
			for _, e := range c.Env {
				if e.Name == "OPERATOR_IMAGE" {
					return c.Image, e.Value
				}
			}
			t.Fatal("manager container has no OPERATOR_IMAGE")
		}
	}
	t.Fatal("no manager container in the build")
	return "", ""
}

// makeImageNames returns the image names the Makefile passes to
// `kustomize edit set image <name>=${IMG}` inside config/<dir>.
func makeImageNames(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`cd config/` + dir + ` && \$\(KUSTOMIZE\) edit set image (\S+)=\$\{IMG\}`)
	var names []string
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	if len(names) == 0 {
		t.Fatalf("Makefile sets no image in config/%s", dir)
	}
	return names
}

// The reboot Pod runs OPERATOR_IMAGE, so it has to be the image the manager
// itself runs however the manifests are built.
func TestOperatorImageFollowsManagerImage(t *testing.T) {
	bin := kustomizeBin(t)

	b, err := os.ReadFile("manager/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var k struct {
		Images []struct {
			NewName string `json:"newName"`
			NewTag  string `json:"newTag"`
		} `json:"images"`
	}
	if err := yaml.Unmarshal(b, &k); err != nil {
		t.Fatal(err)
	}
	if len(k.Images) != 1 {
		t.Fatalf("config/manager has %d images entries, want 1", len(k.Images))
	}
	released := k.Images[0]

	check := func(t *testing.T, build []byte, want string) {
		t.Helper()
		image, operatorImage := managerImages(t, build)
		if image != want {
			t.Errorf("manager image = %q, want %q", image, want)
		}
		if operatorImage != want {
			t.Errorf("OPERATOR_IMAGE = %q, want %q", operatorImage, want)
		}
	}

	t.Run("default", func(t *testing.T) {
		root := copyConfig(t)
		check(t, kustomize(t, bin, root, "build", "config/default"), released.NewName+":"+released.NewTag)
	})

	for _, name := range makeImageNames(t, "manager") {
		t.Run("make deploy IMG= via "+name, func(t *testing.T) {
			root := copyConfig(t)
			kustomize(t, bin, filepath.Join(root, "config/manager"), "edit", "set", "image", name+"=example.com/me/operator:dev")
			check(t, kustomize(t, bin, root, "build", "config/default"), "example.com/me/operator:dev")
		})
	}

	t.Run("dev", func(t *testing.T) {
		root := copyConfig(t)
		check(t, kustomize(t, bin, root, "build", "config/dev"), "controller:latest")
	})

	for _, name := range makeImageNames(t, "dev") {
		t.Run("make deploy-dev IMG= via "+name, func(t *testing.T) {
			root := copyConfig(t)
			kustomize(t, bin, filepath.Join(root, "config/dev"), "edit", "set", "image", name+"=example.com/me/operator:e2e")
			check(t, kustomize(t, bin, root, "build", "config/dev"), "example.com/me/operator:e2e")
		})
	}

	t.Run("overlay images remap", func(t *testing.T) {
		root := copyConfig(t)
		overlay := filepath.Join(root, "overlay")
		if err := os.Mkdir(overlay, 0o755); err != nil {
			t.Fatal(err)
		}
		kust := "resources:\n- ../config/default\nimages:\n- name: " + released.NewName + "\n  newName: mirror.example.com/kairos/operator\n"
		if err := os.WriteFile(filepath.Join(overlay, "kustomization.yaml"), []byte(kust), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, kustomize(t, bin, root, "build", "overlay"), "mirror.example.com/kairos/operator:"+released.NewTag)
	})
}
