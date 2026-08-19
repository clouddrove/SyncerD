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

func TestASingleOversizedLineDoesNotProduceAnEmptyBlock(t *testing.T) {
	// Block Kit rejects a section with empty text, and the failure
	// notification is exactly where an enormous line shows up: a transport
	// error carrying a multi-kilobyte HTML body. An empty block meant Slack
	// answered 400 and the whole alert was lost.
	msg := Message{
		Title: "sync failed",
		Sections: []Section{{
			Lines: []string{strings.Repeat("x", maxSectionChars+500)},
		}},
	}

	c := &SlackClient{}
	blocks := c.renderBlocks(msg)
	for i, b := range blocks {
		if b.Type != "section" {
			continue
		}
		if b.Text == nil || b.Text.Text == "" {
			t.Fatalf("block %d is an empty section, which Slack rejects", i)
		}
		// Slack counts characters, not bytes.
		if n := len([]rune(b.Text.Text)); n > maxSectionChars {
			t.Errorf("block %d is %d characters, over the limit", i, n)
		}
	}
}

func TestTruncateCountsCharactersAndKeepsRunesWhole(t *testing.T) {
	body := strings.Repeat("字", 50)
	if got := truncate(body, 100); got != body {
		t.Error("a string inside the limit must be untouched")
	}

	got := truncate(strings.Repeat("字", 200), 100)
	if n := len([]rune(got)); n > 100 {
		t.Errorf("truncated to %d characters, over the limit", n)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("truncation split a multi byte rune")
	}
}
