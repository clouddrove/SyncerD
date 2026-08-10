package vcs

import (
	"fmt"
	"strings"
	"text/template"
)

// DefaultNameTemplate maps a source repository to a destination name using
// the bare repository name.
const DefaultNameTemplate = "{{ .Repo }}"

// NameTemplate renders destination repository names from source repos.
type NameTemplate struct {
	raw  string
	tmpl *template.Template
}

// nameVars are the variables exposed to a name template.
type nameVars struct {
	Repo  string
	Owner string
	Path  string
}

// ParseNameTemplate compiles raw. An empty string yields DefaultNameTemplate.
// Unknown variables are a render-time error rather than silently empty.
func ParseNameTemplate(raw string) (*NameTemplate, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultNameTemplate
	}
	t, err := template.New("name").Option("missingkey=error").Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse name_template %q: %w", raw, err)
	}
	return &NameTemplate{raw: raw, tmpl: t}, nil
}

// Raw returns the template source.
func (n *NameTemplate) Raw() string { return n.raw }

// UsesPath reports whether the template references .Path, which produces a
// nested name that flat destinations cannot accept.
func (n *NameTemplate) UsesPath() bool {
	return strings.Contains(n.raw, ".Path")
}

// Render produces the destination repository name for r.
func (n *NameTemplate) Render(r Repo) (string, error) {
	var sb strings.Builder
	err := n.tmpl.Execute(&sb, nameVars{Repo: r.Name, Owner: r.Owner, Path: r.Path})
	if err != nil {
		return "", fmt.Errorf("render name_template %q: %w", n.raw, err)
	}
	out := strings.Trim(strings.TrimSpace(sb.String()), "/")
	if out == "" {
		return "", fmt.Errorf("name_template %q rendered empty for repo %q", n.raw, r.Name)
	}
	return out, nil
}
