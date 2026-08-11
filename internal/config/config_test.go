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
