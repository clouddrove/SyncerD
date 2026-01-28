package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	Source      SourceConfig      `mapstructure:"source"`
	Destinations []DestinationConfig `mapstructure:"destinations"`
	Images      []ImageConfig     `mapstructure:"images"`
	Schedule    string            `mapstructure:"schedule"`
	StatePath   string            `mapstructure:"state_path"`
	Slack       SlackConfig        `mapstructure:"slack"`
	FailFast    bool              `mapstructure:"fail_fast"`
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
	Name      string   `mapstructure:"name"`      // e.g., "library/nginx"
	Tags      []string `mapstructure:"tags"`      // specific tags to sync, empty means all
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

func Load(configPath string) (*Config, error) {
	viper.SetConfigType("yaml")
	viper.SetConfigName("syncerd")

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		// Look for config in current directory
		viper.AddConfigPath(".")
		viper.AddConfigPath("./config")
	}

	// Environment variables
	viper.SetEnvPrefix("SYNCERD")
	viper.AutomaticEnv()

	// Set defaults
	viper.SetDefault("source.type", "dockerhub")
	viper.SetDefault("source.registry", "docker.io")
	viper.SetDefault("schedule", "0 0 */21 * *") // Every 3 weeks
	viper.SetDefault("state_path", ".syncerd-state.json")
	viper.SetDefault("slack.enabled", false)
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

	// Override with environment variables if set
	if username := os.Getenv("SYNCERD_SOURCE_USERNAME"); username != "" {
		cfg.Source.Username = username
	}
	if password := os.Getenv("SYNCERD_SOURCE_PASSWORD"); password != "" {
		cfg.Source.Password = password
	}
	if token := os.Getenv("SYNCERD_SOURCE_TOKEN"); token != "" {
		cfg.Source.Token = token
	}
	if statePath := os.Getenv("SYNCERD_STATE_PATH"); statePath != "" {
		cfg.StatePath = statePath
	}
	if slackWebhook := os.Getenv("SYNCERD_SLACK_WEBHOOK_URL"); slackWebhook != "" {
		cfg.Slack.WebhookURL = slackWebhook
	}
	if slackChannel := os.Getenv("SYNCERD_SLACK_CHANNEL"); slackChannel != "" {
		cfg.Slack.Channel = slackChannel
	}
	if slackFormat := os.Getenv("SYNCERD_SLACK_MESSAGE_FORMAT"); slackFormat != "" {
		cfg.Slack.MessageFormat = slackFormat
	}
	if failFast := os.Getenv("SYNCERD_FAIL_FAST"); failFast != "" {
		// treat any non-empty, non-"0", non-"false" as true
		switch failFast {
		case "0", "false", "FALSE", "False":
			cfg.FailFast = false
		default:
			cfg.FailFast = true
		}
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Source.Type == "" {
		return fmt.Errorf("source.type is required")
	}

	if len(c.Destinations) == 0 {
		return fmt.Errorf("at least one destination is required")
	}

	if len(c.Images) == 0 {
		return fmt.Errorf("at least one image is required")
	}

	for i, dest := range c.Destinations {
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

func GetDefaultConfigPath() string {
	// Check current directory first
	if _, err := os.Stat("syncerd.yaml"); err == nil {
		return "syncerd.yaml"
	}
	if _, err := os.Stat("syncerd.yml"); err == nil {
		return "syncerd.yml"
	}
	return filepath.Join(".", "syncerd.yaml")
}
