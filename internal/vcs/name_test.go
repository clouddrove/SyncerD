package vcs

import (
	"strings"
	"testing"
)

var sample = Repo{Owner: "clouddrove", Name: "terraform-aws-vpc", Path: "clouddrove/terraform-aws-vpc"}

func TestParseEmptyUsesDefault(t *testing.T) {
	tpl, err := ParseNameTemplate("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tpl.Raw() != DefaultNameTemplate {
		t.Errorf("Raw() = %q, want %q", tpl.Raw(), DefaultNameTemplate)
	}
	got, err := tpl.Render(sample)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "terraform-aws-vpc" {
		t.Errorf("Render() = %q, want terraform-aws-vpc", got)
	}
	tplSpace, err := ParseNameTemplate("   ")
	if err != nil {
		t.Fatalf("parse whitespace: %v", err)
	}
	if tplSpace.Raw() != DefaultNameTemplate {
		t.Errorf("Raw() for whitespace = %q, want %q", tplSpace.Raw(), DefaultNameTemplate)
	}
}

func TestRenderVariables(t *testing.T) {
	cases := map[string]string{
		"{{ .Repo }}":              "terraform-aws-vpc",
		"{{ .Owner }}":             "clouddrove",
		"{{ .Path }}":              "clouddrove/terraform-aws-vpc",
		"ado-{{ .Repo }}":          "ado-terraform-aws-vpc",
		"{{ .Owner }}-{{ .Repo }}": "clouddrove-terraform-aws-vpc",
	}
	for raw, want := range cases {
		tpl, err := ParseNameTemplate(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		got, err := tpl.Render(sample)
		if err != nil {
			t.Fatalf("render %q: %v", raw, err)
		}
		if got != want {
			t.Errorf("Render(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseRejectsBadSyntax(t *testing.T) {
	if _, err := ParseNameTemplate("{{ .Repo "); err == nil {
		t.Fatal("expected parse error for unterminated action")
	}
}

func TestParseRejectsUnknownVariable(t *testing.T) {
	tpl, err := ParseNameTemplate("{{ .Nope }}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := tpl.Render(sample); err == nil {
		t.Fatal("expected render error for unknown variable")
	}
}

func TestRenderRejectsEmptyResult(t *testing.T) {
	tpl, err := ParseNameTemplate("{{ .Repo }}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := tpl.Render(Repo{}); err == nil {
		t.Fatal("expected error for empty rendered name")
	}
}

func TestProducesNestedName(t *testing.T) {
	cases := map[string]bool{
		"{{ .Path }}":              true,
		"{{ .Owner }}/{{ .Repo }}": true,
		"team/{{ .Repo }}":         true,
		"{{ .Repo }}":              false,
		"{{ .Owner }}-{{ .Repo }}": false,
		"ado-{{ .Repo }}":          false,
		"":                         false,
	}
	for raw, want := range cases {
		tpl, err := ParseNameTemplate(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if got := tpl.ProducesNestedName(); got != want {
			t.Errorf("ProducesNestedName(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestRenderAcceptsLegitimateSeparatorNames pins that Render judges a
// rendered name by whether a template variable was actually empty, not by
// whether the output happens to start or end with a separator character.
// ".github" is a real, common repository name and must be accepted even
// though it begins with a dot; a template that references an empty field,
// such as ".Owner" against a CodeCommit source, must still be rejected.
func TestRenderAcceptsLegitimateSeparatorNames(t *testing.T) {
	cases := []struct {
		template string
		repo     Repo
		want     string
	}{
		{template: "{{ .Repo }}", repo: Repo{Owner: "acme", Name: ".github", Path: "acme/.github"}, want: ".github"},
		{template: "{{ .Repo }}", repo: Repo{Owner: "acme", Name: "_shared", Path: "acme/_shared"}, want: "_shared"},
		{template: "{{ .Repo }}", repo: Repo{Owner: "acme", Name: "dotfiles.", Path: "acme/dotfiles."}, want: "dotfiles."},
		{template: "{{ .Repo }}", repo: Repo{Owner: "acme", Name: "terraform-aws-vpc", Path: "acme/terraform-aws-vpc"}, want: "terraform-aws-vpc"},
		{template: "{{ .Repo }}", repo: Repo{Owner: "acme", Name: "x", Path: "acme/x"}, want: "x"},
		{template: "{{ .Owner }}-{{ .Repo }}", repo: Repo{Owner: "acme", Name: "svc", Path: "acme/svc"}, want: "acme-svc"},
	}
	for _, tc := range cases {
		tpl, err := ParseNameTemplate(tc.template)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.template, err)
		}
		got, err := tpl.Render(tc.repo)
		if err != nil {
			t.Errorf("Render(%q) against %+v: unexpected error: %v", tc.template, tc.repo, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Render(%q) against %+v = %q, want %q", tc.template, tc.repo, got, tc.want)
		}
	}
}

// TestRenderRejectsEmptyTemplateVariable pins the guard that catches a
// CodeCommit source, which has no owner: a template like
// "{{ .Owner }}-{{ .Repo }}" must be rejected rather than silently
// rendering "-svc", because the empty variable, not the leading or
// trailing separator, is the actual defect.
func TestRenderRejectsEmptyTemplateVariable(t *testing.T) {
	cases := []struct {
		template string
		repo     Repo
	}{
		{template: "{{ .Owner }}-{{ .Repo }}", repo: Repo{Name: "svc", Path: "svc"}},
		{template: "{{ .Owner }}_{{ .Repo }}", repo: Repo{Name: "svc", Path: "svc"}},
		{template: "{{ .Repo }}-{{ .Owner }}", repo: Repo{Name: "svc", Path: "svc"}},
	}
	for _, tc := range cases {
		tpl, err := ParseNameTemplate(tc.template)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.template, err)
		}
		if _, err := tpl.Render(tc.repo); err == nil {
			t.Errorf("Render(%q) against %+v with an empty owner should have been rejected", tc.template, tc.repo)
		}
	}
}

// TestRenderDotGithubReachesEngine is the regression path for the wrong
// fix that was briefly in place: a CodeCommit-shaped source (empty Owner)
// with a repository literally named ".github", rendered through the
// default template, must succeed end to end rather than being rejected
// merely because the output starts with a separator character.
func TestRenderDotGithubReachesEngine(t *testing.T) {
	tpl, err := ParseNameTemplate(DefaultNameTemplate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	codeCommitRepo := Repo{Owner: "", Name: ".github", Path: ".github"}
	got, err := tpl.Render(codeCommitRepo)
	if err != nil {
		t.Fatalf("Render(.github) from a CodeCommit-shaped source must succeed, got %v", err)
	}
	if got != ".github" {
		t.Errorf("Render() = %q, want .github", got)
	}
}

func TestRenderKeepsNestedPath(t *testing.T) {
	tpl, _ := ParseNameTemplate("{{ .Path }}")
	got, err := tpl.Render(sample)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "/") {
		t.Fatal("fixture should render a nested path")
	}
}
