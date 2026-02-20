package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("renderOCIBuildTemplate", func() {
	When("the build definition contains no template directives", func() {
		It("returns the content unchanged", func() {
			content := "FROM ubuntu:22.04\nRUN apt-get update\n"
			result, err := renderOCIBuildTemplate(content, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(content))
		})
	})

	When("the build definition contains template variables but no values are provided", func() {
		It("renders variables as empty strings", func() {
			content := "FROM {{ .ImageBase }}\nRUN echo hello\n"
			result, err := renderOCIBuildTemplate(content, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM \nRUN echo hello\n"))
		})
	})

	When("the build definition contains template variables and values are provided", func() {
		It("renders the template with the provided values", func() {
			content := "FROM {{ .ImageBase }}\nRUN {{ .InstallCmd }}\n"
			values := map[string]string{
				"ImageBase":  "ubuntu:22.04",
				"InstallCmd": "apt-get update && apt-get install -y curl",
			}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\nRUN apt-get update && apt-get install -y curl\n"))
		})
	})

	When("the build definition has an invalid template syntax", func() {
		It("returns an error", func() {
			content := "FROM {{ .ImageBase }\n"
			_, err := renderOCIBuildTemplate(content, nil)
			Expect(err).To(HaveOccurred())
		})
	})

	When("the template references a key not in values", func() {
		It("renders the missing key as an empty string", func() {
			content := "FROM {{ .ImageBase }}\nLABEL version={{ .Version }}\n"
			values := map[string]string{
				"ImageBase": "alpine:3.18",
			}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM alpine:3.18\nLABEL version=\n"))
		})
	})

	When("the template tries to use disallowed actions", func() {
		It("rejects templates using 'define'", func() {
			content := `{{ define "evil" }}something{{ end }}FROM ubuntu`
			_, err := renderOCIBuildTemplate(content, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})

		It("rejects templates using 'template'", func() {
			content := `{{ template "something" }}FROM ubuntu`
			_, err := renderOCIBuildTemplate(content, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})

		It("rejects templates using 'block'", func() {
			content := `{{ block "myblock" . }}content{{ end }}FROM ubuntu`
			_, err := renderOCIBuildTemplate(content, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})
	})

	When("the template uses conditionals and ranges", func() {
		It("allows if/else", func() {
			content := "FROM {{ if .ImageBase }}{{ .ImageBase }}{{ else }}ubuntu:latest{{ end }}\n"
			values := map[string]string{}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:latest\n"))
		})

		It("allows if with a provided value", func() {
			content := "FROM {{ if .ImageBase }}{{ .ImageBase }}{{ else }}ubuntu:latest{{ end }}\n"
			values := map[string]string{"ImageBase": "alpine:3.18"}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM alpine:3.18\n"))
		})

		It("allows range over values", func() {
			content := "FROM ubuntu:22.04\n{{range $key, $val := .}}ENV {{$key}}={{$val}}\n{{end}}"
			values := map[string]string{
				"APP_ENV":   "production",
				"LOG_LEVEL": "info",
			}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			// The order of map iteration is not guaranteed, so check both possibilities
			Expect(result).To(Or(
				Equal("FROM ubuntu:22.04\nENV APP_ENV=production\nENV LOG_LEVEL=info\n"),
				Equal("FROM ubuntu:22.04\nENV LOG_LEVEL=info\nENV APP_ENV=production\n"),
			))
		})

		It("allows with to set context", func() {
			content := "FROM ubuntu:22.04\n{{with .ImageBase}}LABEL base={{.}}{{end}}\n"
			values := map[string]string{
				"ImageBase": "ubuntu:22.04",
			}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\nLABEL base=ubuntu:22.04\n"))
		})

		It("allows with to skip block when value is empty", func() {
			content := "FROM ubuntu:22.04\n{{with .ImageBase}}LABEL base={{.}}{{end}}\nRUN echo done\n"
			values := map[string]string{}
			result, err := renderOCIBuildTemplate(content, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\n\nRUN echo done\n"))
		})
	})
})
