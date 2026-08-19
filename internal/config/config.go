package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Source       SourceConfig        `mapstructure:"source"`
	Destinations []DestinationConfig `mapstructure:"destinations"`
	Images       []ImageConfig       `mapstructure:"images"`
	Schedule     string              `mapstructure:"schedule"`
	StatePath    string              `mapstructure:"state_path"`
	Slack        SlackConfig         `mapstructure:"slack"`
	FailFast     bool                `mapstructure:"fail_fast"`
	Git          *GitConfig          `mapstructure:"git"`
}

type SourceConfig struct {
	Type     string `mapstructure:"type"` // "dockerhub"
	Registry string `mapstructure:"registry"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	Token    string `mapstructure:"token"`
}

type DestinationConfig struct {
	Name     string            `mapstructure:"name"`
	Type     string            `mapstructure:"type"` // "ecr", "acr", "gcr", "ghcr"
	Registry string            `mapstructure:"registry"`
	Region   string            `mapstructure:"region,omitempty"`
	Auth     map[string]string `mapstructure:"auth"`
}

type ImageConfig struct {
	Name      string   `mapstructure:"name"`       // e.g., "library/nginx"
	Tags      []string `mapstructure:"tags"`       // specific tags to sync, empty means all
	WatchTags bool     `mapstructure:"watch_tags"` // watch for new tags
}

type SlackConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	WebhookURL  string `mapstructure:"webhook_url"`
	Channel     string `mapstructure:"channel"`
	Username    string `mapstructure:"username"`
	IconEmoji   string `mapstructure:"icon_emoji"`
	NotifyOnNew bool   `mapstructure:"notify_on_new"`
	NotifyOnErr bool   `mapstructure:"notify_on_error"`
	// MessageFormat controls Slack message verbosity:
	// - "compact": short summary (default)
	// - "detailed": grouped + counts + full listing (capped)
	MessageFormat string `mapstructure:"message_format"`
}

// configSearchPaths are the directories searched for a config file, and
// configSearchNames the file names accepted in each, in order.
var (
	configSearchPaths = []string{".", "./config"}
	configSearchNames = []string{"syncerd.yaml", "syncerd.yml"}
)

// findConfigFile returns the first config file present, or an error naming
// everywhere it looked.
//
// Only a real extension counts. An extensionless "syncerd" is the compiled
// binary, not configuration.
func findConfigFile() (string, error) {
	var tried []string
	for _, dir := range configSearchPaths {
		for _, name := range configSearchNames {
			candidate := filepath.Join(dir, name)
			tried = append(tried, candidate)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no config file found; looked for %s. Pass --config to name one explicitly",
		strings.Join(tried, ", "))
}

func Load(configPath string) (*Config, error) {
	viper.SetConfigType("yaml")
	viper.SetConfigName("syncerd")

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		// Resolve the file here rather than letting viper search.
		//
		// viper matches an extensionless file when a config type is set,
		// and the file it finds in a working directory is very often the
		// syncerd binary itself: `make build` writes ./syncerd, and running
		// ./syncerd sync in that directory made the tool parse its own
		// binary as YAML. The error that produced, "control characters are
		// not allowed", says nothing about what actually happened, and the
		// project's own scheduled workflow failed that way for months.
		found, err := findConfigFile()
		if err != nil {
			return nil, err
		}
		viper.SetConfigFile(found)
	}

	// Environment variables: SYNCERD_ prefix, with nested keys mapped via
	// dot->underscore (e.g. source.username <- SYNCERD_SOURCE_USERNAME).
	viper.SetEnvPrefix("SYNCERD")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	// Explicitly bind nested keys so they resolve during Unmarshal even when
	// no config file or default is present for them.
	for _, key := range []string{
		"source.username",
		"source.password",
		"source.token",
		"state_path",
		"slack.enabled",
		"slack.webhook_url",
		"slack.channel",
		"slack.notify_on_new",
		"slack.notify_on_error",
		"slack.message_format",
		"fail_fast",
	} {
		_ = viper.BindEnv(key)
	}

	// Set defaults
	viper.SetDefault("source.type", "dockerhub")
	viper.SetDefault("source.registry", "docker.io")
	viper.SetDefault("schedule", "0 0 */21 * *") // Every 3 weeks
	viper.SetDefault("state_path", ".syncerd-state.json")
	// Slack is opt-out: configuring a webhook URL enables notifications.
	// Set slack.enabled (or SYNCERD_SLACK_ENABLED) to false to suppress them.
	viper.SetDefault("slack.enabled", true)
	viper.SetDefault("slack.notify_on_new", true)
	viper.SetDefault("slack.notify_on_error", true)
	viper.SetDefault("slack.username", "SyncerD")
	viper.SetDefault("slack.icon_emoji", ":whale:")
	viper.SetDefault("slack.message_format", "compact")
	viper.SetDefault("fail_fast", false)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found, use defaults and env vars
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	if cfg.Git != nil {
		cfg.Git.ApplyEnvOverlay()
		cfg.Git.ApplyDefaults()
	}

	return &cfg, nil
}

// ValidateImageSync checks the configuration required by the "sync" command.
// Git mirroring has its own validator; see ValidateGitSync.
func (c *Config) ValidateImageSync() error {
	if c.Source.Type == "" {
		return fmt.Errorf("source.type is required")
	}

	if len(c.Destinations) == 0 {
		return fmt.Errorf("at least one destination is required")
	}

	if len(c.Images) == 0 {
		return fmt.Errorf("at least one image is required")
	}

	seenNames := make(map[string]int, len(c.Destinations))
	for i, dest := range c.Destinations {
		if dest.Name == "" {
			return fmt.Errorf("destinations[%d].name is required", i)
		}
		if j, ok := seenNames[dest.Name]; ok {
			return fmt.Errorf("destinations[%d].name %q duplicates destinations[%d].name; names must be unique (used as the sync state key)", i, dest.Name, j)
		}
		seenNames[dest.Name] = i
		if dest.Type == "" {
			return fmt.Errorf("destinations[%d].type is required", i)
		}
		if dest.Registry == "" {
			return fmt.Errorf("destinations[%d].registry is required", i)
		}
	}

	for i, img := range c.Images {
		if img.Name == "" {
			return fmt.Errorf("images[%d].name is required", i)
		}
	}

	return nil
}
