package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// writeConfig writes a config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "syncerd.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDoesNotValidate(t *testing.T) {
	viper.Reset()
	// No destinations and no images. Load must succeed; only
	// ValidateImageSync may reject this.
	path := writeConfig(t, "source:\n  type: dockerhub\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load must not validate, got error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
}

func TestValidateImageSyncRequiresDestinations(t *testing.T) {
	cfg := &Config{Source: SourceConfig{Type: "dockerhub"}}
	err := cfg.ValidateImageSync()
	if err == nil || !strings.Contains(err.Error(), "at least one destination is required") {
		t.Fatalf("expected destination error, got %v", err)
	}
}

func TestValidateImageSyncRequiresImages(t *testing.T) {
	cfg := &Config{
		Source:       SourceConfig{Type: "dockerhub"},
		Destinations: []DestinationConfig{{Name: "ecr", Type: "ecr", Registry: "x.dkr.ecr.us-east-1.amazonaws.com"}},
	}
	err := cfg.ValidateImageSync()
	if err == nil || !strings.Contains(err.Error(), "at least one image is required") {
		t.Fatalf("expected image error, got %v", err)
	}
}

func TestValidateImageSyncRejectsDuplicateDestinationNames(t *testing.T) {
	cfg := &Config{
		Source: SourceConfig{Type: "dockerhub"},
		Destinations: []DestinationConfig{
			{Name: "ecr", Type: "ecr", Registry: "a"},
			{Name: "ecr", Type: "acr", Registry: "b"},
		},
		Images: []ImageConfig{{Name: "library/nginx"}},
	}
	err := cfg.ValidateImageSync()
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestValidateImageSyncAcceptsValidConfig(t *testing.T) {
	cfg := &Config{
		Source:       SourceConfig{Type: "dockerhub"},
		Destinations: []DestinationConfig{{Name: "ecr", Type: "ecr", Registry: "a"}},
		Images:       []ImageConfig{{Name: "library/nginx"}},
	}
	if err := cfg.ValidateImageSync(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestABinaryNamedSyncerdIsNotTreatedAsConfig(t *testing.T) {
	// `make build` writes ./syncerd, and viper matches an extensionless
	// file when a config type is set, so running ./syncerd sync in that
	// directory made the tool parse its own binary as YAML. The error was
	// "control characters are not allowed", which points at nothing, and
	// the project's own scheduled workflow failed that way for months.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "syncerd"), []byte("\x7fELF\x02\x01\x01\x00binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	_, err = findConfigFile()
	if err == nil {
		t.Fatal("an extensionless binary must not be accepted as a config file")
	}
	if !strings.Contains(err.Error(), "no config file found") {
		t.Errorf("the error should say what is missing, got %v", err)
	}
}

func TestFindConfigFilePrefersAnExplicitYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "syncerd"), []byte("binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "syncerd.yaml"), []byte("source:\n  type: dockerhub\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	got, err := findConfigFile()
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if filepath.Base(got) != "syncerd.yaml" {
		t.Errorf("found %q, want syncerd.yaml", got)
	}
}
