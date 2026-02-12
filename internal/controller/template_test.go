package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("renderDockerfileTemplate", func() {
	When("the Dockerfile contains no template directives", func() {
		It("returns the Dockerfile unchanged", func() {
			dockerfile := "FROM ubuntu:22.04\nRUN apt-get update\n"
			result, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(dockerfile))
		})
	})

	When("the Dockerfile contains template variables but no values are provided", func() {
		It("renders variables as empty strings", func() {
			dockerfile := "FROM {{ .ImageBase }}\nRUN echo hello\n"
			result, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM \nRUN echo hello\n"))
		})
	})

	When("the Dockerfile contains template variables and values are provided", func() {
		It("renders the template with the provided values", func() {
			dockerfile := "FROM {{ .ImageBase }}\nRUN {{ .InstallCmd }}\n"
			values := map[string]string{
				"ImageBase":  "ubuntu:22.04",
				"InstallCmd": "apt-get update && apt-get install -y curl",
			}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\nRUN apt-get update && apt-get install -y curl\n"))
		})
	})

	When("the Dockerfile has an invalid template syntax", func() {
		It("returns an error", func() {
			dockerfile := "FROM {{ .ImageBase }\n"
			_, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).To(HaveOccurred())
		})
	})

	When("the template references a key not in values", func() {
		It("renders the missing key as an empty string", func() {
			dockerfile := "FROM {{ .ImageBase }}\nLABEL version={{ .Version }}\n"
			values := map[string]string{
				"ImageBase": "alpine:3.18",
			}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM alpine:3.18\nLABEL version=\n"))
		})
	})

	When("the template tries to use disallowed actions", func() {
		It("rejects templates using 'define'", func() {
			dockerfile := `{{ define "evil" }}something{{ end }}FROM ubuntu`
			_, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})

		It("rejects templates using 'template'", func() {
			dockerfile := `{{ template "something" }}FROM ubuntu`
			_, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})

		It("rejects templates using 'block'", func() {
			dockerfile := `{{ block "myblock" . }}content{{ end }}FROM ubuntu`
			_, err := renderDockerfileTemplate(dockerfile, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
		})
	})

	When("the template uses conditionals and ranges", func() {
		It("allows if/else", func() {
			dockerfile := "FROM {{ if .ImageBase }}{{ .ImageBase }}{{ else }}ubuntu:latest{{ end }}\n"
			values := map[string]string{}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:latest\n"))
		})

		It("allows if with a provided value", func() {
			dockerfile := "FROM {{ if .ImageBase }}{{ .ImageBase }}{{ else }}ubuntu:latest{{ end }}\n"
			values := map[string]string{"ImageBase": "alpine:3.18"}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM alpine:3.18\n"))
		})

		It("allows range over the values map", func() {
			dockerfile := "FROM ubuntu:22.04\n{{ range $key, $value := . }}ENV {{ $key }}={{ $value }}\n{{ end }}"
			values := map[string]string{
				"VAR1": "value1",
				"VAR2": "value2",
			}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			// Note: map iteration order is not guaranteed in Go, so we check for both lines
			Expect(result).To(ContainSubstring("ENV VAR1=value1"))
			Expect(result).To(ContainSubstring("ENV VAR2=value2"))
			Expect(result).To(HavePrefix("FROM ubuntu:22.04\n"))
		})

		It("allows with to set context", func() {
			dockerfile := "FROM ubuntu:22.04\n{{ with .ImageTag }}RUN echo Building tag: {{ . }}\n{{ end }}"
			values := map[string]string{"ImageTag": "v1.2.3"}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\nRUN echo Building tag: v1.2.3\n"))
		})

		It("allows with when context value is empty", func() {
			dockerfile := "FROM ubuntu:22.04\n{{ with .ImageTag }}RUN echo Building tag: {{ . }}\n{{ end }}RUN echo Done\n"
			values := map[string]string{}
			result, err := renderDockerfileTemplate(dockerfile, values)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("FROM ubuntu:22.04\nRUN echo Done\n"))
		})
	})
})
