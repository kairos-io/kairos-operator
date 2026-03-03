package controller

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
)

// forbiddenDirectivesRE matches Go template directives that are not useful
// in OCI build definition templates and would likely cause confusing errors:
// define, template, and block.
// There is no security issue here since the author of the build definition is
// the same as the author of the values and most likely the operator of the cluster.
var forbiddenDirectivesRE = regexp.MustCompile(`\{\{-?\s*(define|template|block)\s`)

// renderOCIBuildTemplate renders an OCI build definition (containerfile format)
// that contains Go template syntax using the provided key-value pairs. Only simple
// value substitution and basic control flow (if/else/range/with) are allowed.
// The directives "define", "template", and "block" are explicitly forbidden.
//
// Missing keys render as empty strings (zero value for string).
func renderOCIBuildTemplate(content string, values map[string]string) (string, error) {
	if forbiddenDirectivesRE.MatchString(content) {
		return "", fmt.Errorf("forbidden template directive detected: define, template, and block are not supported in OCI build templates")
	}

	// Use Option("missingkey=zero") so that referencing a key that doesn't
	// exist in the values map produces an empty string instead of an error.
	tmpl, err := template.New("ocispec").
		Option("missingkey=zero").
		Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse OCI build template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("failed to render OCI build template: %w", err)
	}

	return buf.String(), nil
}
