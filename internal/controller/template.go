package controller

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
)

// forbiddenDirectivesRE matches Go template directives that are not useful
// in Dockerfile templates and would likely cause confusing errors: define,
// template, and block.
// There is not security issue here since the author of the Dockerfile is the same
// as the author of the values and most likely the operator of the cluster on
// which the image is built.
var forbiddenDirectivesRE = regexp.MustCompile(`\{\{-?\s*(define|template|block)\s`)

// renderDockerfileTemplate renders a Dockerfile that contains Go template
// syntax using the provided key-value pairs. Only simple value substitution
// and basic control flow (if/else/range/with) are allowed. The directives
// "define", "template", and "block" are explicitly forbidden as they are not
// meaningful in this context and would lead to confusing behavior.
//
// Missing keys render as empty strings (zero value for string).
func renderDockerfileTemplate(dockerfile string, values map[string]string) (string, error) {
	if forbiddenDirectivesRE.MatchString(dockerfile) {
		return "", fmt.Errorf("forbidden template directive detected: define, template, and block are not supported in Dockerfile templates")
	}

	// Use Option("missingkey=zero") so that referencing a key that doesn't
	// exist in the values map produces an empty string instead of an error.
	tmpl, err := template.New("Dockerfile").
		Option("missingkey=zero").
		Parse(dockerfile)
	if err != nil {
		return "", fmt.Errorf("failed to parse Dockerfile template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("failed to render Dockerfile template: %w", err)
	}

	return buf.String(), nil
}
