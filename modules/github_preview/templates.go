package github_preview

import (
	"text/template"
	"io"
	"os"
)

const defaultErrorTemplate = `preview not available{{if .PR}} for PR #{{.PR}}{{end}}{{if .Host}} ({{.Host}}){{end}}: {{.Error}}
`

type errorData struct {
	PR    int
	Host  string
	Error string
}

type templateRenderer struct {
	errorTmpl *template.Template
}

func newTemplateRenderer(inlineTemplate string, templateFile string) (*templateRenderer, error) {
	tmplStr := defaultErrorTemplate

	if templateFile != "" {
		data, err := os.ReadFile(templateFile)
		if err != nil {
			return nil, err
		}
		tmplStr = string(data)
	} else if inlineTemplate != "" {
		tmplStr = inlineTemplate
	}

	t, err := template.New("error").Parse(tmplStr)
	if err != nil {
		return nil, err
	}

	return &templateRenderer{errorTmpl: t}, nil
}

func (t *templateRenderer) renderError(w io.Writer, data errorData) {
	t.errorTmpl.Execute(w, data)
}
