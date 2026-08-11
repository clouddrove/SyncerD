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
