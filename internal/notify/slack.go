package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type SlackClient struct {
	WebhookURL string
	Channel    string
	Username   string
	IconEmoji  string
	HTTP       *http.Client
}

type slackPayload struct {
	Text      string `json:"text"`
	Channel   string `json:"channel,omitempty"`
	Username  string `json:"username,omitempty"`
	IconEmoji string `json:"icon_emoji,omitempty"`
}

func (c *SlackClient) SendText(ctx context.Context, text string) error {
	if c == nil || c.WebhookURL == "" {
		return nil
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	payload := slackPayload{
		Text:      text,
		Channel:   c.Channel,
		Username:  c.Username,
		IconEmoji: c.IconEmoji,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %s", resp.Status)
	}
	return nil
}

