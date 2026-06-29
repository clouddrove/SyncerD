package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CloudDrove branding used in every notification footer.
const (
	brandName = "CloudDrove"
	brandURL  = "https://clouddrove.com"
	// SyncerD product logo, used as the alert thumbnail and footer icon.
	syncerdLogoURL = "https://raw.githubusercontent.com/clouddrove/SyncerD/master/assets/syncerd-logo.png"

	// Attachment color bars.
	ColorSuccess = "#2EB67D" // green
	ColorFailure = "#E01E5A" // red

	// Slack limits we stay under.
	maxHeaderChars  = 150
	maxSectionChars = 2900
)

type SlackClient struct {
	WebhookURL string
	Channel    string
	Username   string
	IconEmoji  string
	HTTP       *http.Client
}

// Section is one logical group in a notification (e.g. a destination and its
// affected image refs). Heading is optional.
type Section struct {
	Heading string
	Lines   []string
}

// Message is a provider-agnostic notification that Send renders into Slack
// Block Kit with consistent CloudDrove branding.
type Message struct {
	Emoji    string // e.g. ":white_check_mark:"
	Title    string // e.g. "3 image(s)/tag(s) synced"
	Color    string // ColorSuccess / ColorFailure
	Fallback string // plain-text summary for notifications/screen readers
	Sections []Section
}

// --- Block Kit payload types ---

type slackPayload struct {
	Channel     string       `json:"channel,omitempty"`
	Username    string       `json:"username,omitempty"`
	IconEmoji   string       `json:"icon_emoji,omitempty"`
	Text        string       `json:"text,omitempty"` // fallback
	Attachments []attachment `json:"attachments,omitempty"`
}

type attachment struct {
	Color  string  `json:"color,omitempty"`
	Blocks []block `json:"blocks,omitempty"`
}

// block is a generic Block Kit block. Fields are pointers/omitempty so a single
// type can represent header, section, divider, and context blocks.
type block struct {
	Type     string      `json:"type"`
	Text     *textObject `json:"text,omitempty"`
	Elements []any       `json:"elements,omitempty"`
}

type textObject struct {
	Type  string `json:"type"` // "plain_text" | "mrkdwn"
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

type imageElement struct {
	Type     string `json:"type"` // "image"
	ImageURL string `json:"image_url"`
	AltText  string `json:"alt_text"`
}

// SendText sends a plain-text message (kept for simple callers/tests).
func (c *SlackClient) SendText(ctx context.Context, text string) error {
	return c.send(ctx, slackPayload{
		Channel:   c.Channel,
		Username:  c.Username,
		IconEmoji: c.IconEmoji,
		Text:      text,
	})
}

// Send renders a Message as a branded Block Kit notification.
func (c *SlackClient) Send(ctx context.Context, m Message) error {
	if c == nil || c.WebhookURL == "" {
		return nil
	}
	return c.send(ctx, slackPayload{
		Channel:     c.Channel,
		Username:    c.Username,
		IconEmoji:   c.IconEmoji,
		Text:        m.Fallback,
		Attachments: []attachment{{Color: m.Color, Blocks: c.renderBlocks(m)}},
	})
}

func (c *SlackClient) renderBlocks(m Message) []block {
	blocks := make([]block, 0, len(m.Sections)+3)

	header := m.Title
	if m.Emoji != "" {
		header = m.Emoji + " " + m.Title
	}
	blocks = append(blocks, block{
		Type: "header",
		Text: &textObject{Type: "plain_text", Text: truncate(header, maxHeaderChars), Emoji: true},
	})

	for _, sec := range m.Sections {
		var buf bytes.Buffer
		if sec.Heading != "" {
			buf.WriteString("*" + sec.Heading + "*\n")
		}
		for _, line := range sec.Lines {
			// Flush into a new section block before exceeding Slack's limit.
			if buf.Len()+len(line)+1 > maxSectionChars {
				blocks = append(blocks, sectionBlock(buf.String()))
				buf.Reset()
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		if buf.Len() > 0 {
			blocks = append(blocks, sectionBlock(buf.String()))
		}
	}

	blocks = append(blocks, block{Type: "divider"})
	blocks = append(blocks, footerBlock())
	return blocks
}

func sectionBlock(text string) block {
	return block{Type: "section", Text: &textObject{Type: "mrkdwn", Text: text}}
}

// footerBlock is the constant CloudDrove-branded context footer.
func footerBlock() block {
	return block{
		Type: "context",
		Elements: []any{
			imageElement{Type: "image", ImageURL: syncerdLogoURL, AltText: "SyncerD"},
			&textObject{Type: "mrkdwn", Text: fmt.Sprintf(
				"*SyncerD* — powered by <%s|%s>", brandURL, brandName)},
		},
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func (c *SlackClient) send(ctx context.Context, payload slackPayload) error {
	if c == nil || c.WebhookURL == "" {
		return nil
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
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
