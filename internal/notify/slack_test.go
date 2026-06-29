package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sampleMessage() Message {
	return Message{
		Emoji:    ":white_check_mark:",
		Title:    "2 image(s)/tag(s) synced",
		Color:    ColorSuccess,
		Fallback: "SyncerD: 2 synced",
		Sections: []Section{{Lines: []string{"• `a`", "• `b`"}}},
	}
}

func TestRenderBlocksStructure(t *testing.T) {
	c := &SlackClient{}
	blocks := c.renderBlocks(sampleMessage())

	if len(blocks) < 3 {
		t.Fatalf("expected header + section + footer, got %d blocks", len(blocks))
	}
	if blocks[0].Type != "header" {
		t.Errorf("first block should be header, got %q", blocks[0].Type)
	}
	if blocks[0].Text == nil || !strings.Contains(blocks[0].Text.Text, "synced") {
		t.Errorf("header text missing title: %+v", blocks[0].Text)
	}

	footer := blocks[len(blocks)-1]
	if footer.Type != "context" {
		t.Fatalf("last block should be context footer, got %q", footer.Type)
	}
	var hasBrand, hasLogo bool
	for _, el := range footer.Elements {
		switch v := el.(type) {
		case *textObject:
			if strings.Contains(v.Text, brandName) && strings.Contains(v.Text, brandURL) {
				hasBrand = true
			}
		case imageElement:
			if v.ImageURL == syncerdLogoURL {
				hasLogo = true
			}
		}
	}
	if !hasBrand {
		t.Errorf("footer missing CloudDrove branding text")
	}
	if !hasLogo {
		t.Errorf("footer missing CloudDrove logo image")
	}
}

func TestFirstSectionHasSyncerdLogoAccessory(t *testing.T) {
	c := &SlackClient{}
	blocks := c.renderBlocks(sampleMessage())
	var found bool
	for _, b := range blocks {
		if b.Type == "section" {
			if b.Accessory == nil || b.Accessory.ImageURL != syncerdLogoURL {
				t.Fatalf("first section missing SyncerD logo accessory: %+v", b.Accessory)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no section block found")
	}
}

func TestSendPostsBlockKitPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &SlackClient{WebhookURL: srv.URL}
	if err := c.Send(context.Background(), sampleMessage()); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	atts, ok := got["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("expected one attachment, got %v", got["attachments"])
	}
	att := atts[0].(map[string]any)
	if att["color"] != ColorSuccess {
		t.Errorf("expected success color, got %v", att["color"])
	}
	if got["text"] != "SyncerD: 2 synced" {
		t.Errorf("expected fallback text, got %v", got["text"])
	}
}

func TestSendNoopWithoutWebhook(t *testing.T) {
	c := &SlackClient{}
	if err := c.Send(context.Background(), sampleMessage()); err != nil {
		t.Errorf("expected nil error with empty webhook, got %v", err)
	}
}

func TestLongSectionSplitsIntoMultipleBlocks(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, "• `registry.example.com/library/some-image:tag-1234567890`")
	}
	c := &SlackClient{}
	blocks := c.renderBlocks(Message{Title: "t", Sections: []Section{{Lines: lines}}})

	sectionCount := 0
	for _, b := range blocks {
		if b.Type == "section" {
			sectionCount++
			if b.Text != nil && len(b.Text.Text) > maxSectionChars {
				t.Errorf("section exceeds Slack limit: %d chars", len(b.Text.Text))
			}
		}
	}
	if sectionCount < 2 {
		t.Errorf("expected long content to split into multiple section blocks, got %d", sectionCount)
	}
}
