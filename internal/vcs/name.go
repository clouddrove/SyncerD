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

// ProducesNestedName reports whether this template can render a name
// containing a slash, which flat destinations cannot accept. It probes the
// template with a fixture repo rather than inspecting the template source,
// so it catches both an explicit .Path reference and a composed name such
// as "{{ .Owner }}/{{ .Repo }}". A template that fails to render is treated
// as nested so the caller rejects it at load time.
func (n *NameTemplate) ProducesNestedName() bool {
	out, err := n.Render(Repo{
		Owner: "probe-owner",
		Name:  "probe-repo",
		Path:  "probe-owner/probe-repo",
	})
	return err != nil || strings.Contains(out, "/")
}

// emptyFieldSentinel is substituted for an empty repository field to
// detect whether the template actually referenced it.
const emptyFieldSentinel = "\x00probe\x00"

// renderWith executes the template against r without any validation.
func (n *NameTemplate) renderWith(r Repo) (string, error) {
	var sb strings.Builder
	if err := n.tmpl.Execute(&sb, nameVars{Repo: r.Name, Owner: r.Owner, Path: r.Path}); err != nil {
		return "", fmt.Errorf("render name_template %q: %w", n.raw, err)
	}
	return strings.Trim(strings.TrimSpace(sb.String()), "/"), nil
}

// Render produces the destination repository name for r.
func (n *NameTemplate) Render(r Repo) (string, error) {
	out, err := n.renderWith(r)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("name_template %q rendered empty for repo %q", n.raw, r.Name)
	}

	// A template that references a field which is empty produces a
	// malformed name, such as "-svc" from "{{ .Owner }}-{{ .Repo }}" when
	// the source has no owner, which is always the case for CodeCommit.
	// Substituting a sentinel for each empty field reveals whether the
	// template actually used one. A name that merely begins or ends with a
	// separator is fine: .github is a real repository.
	probe := r
	probed := false
	if probe.Owner == "" {
		probe.Owner = emptyFieldSentinel
		probed = true
	}
	if probe.Path == "" {
		probe.Path = emptyFieldSentinel
		probed = true
	}
	if probe.Name == "" {
		probe.Name = emptyFieldSentinel
		probed = true
	}
	if probed {
		probeOut, perr := n.renderWith(probe)
		if perr == nil && probeOut != out {
			return "", fmt.Errorf("name_template %q rendered %q for repo %q because a template variable was empty; .Owner is always empty for CodeCommit sources, so use a template that does not reference it", n.raw, out, r.Name)
		}
	}

	return out, nil
}
