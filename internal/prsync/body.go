// Package prsync mirrors pull request objects and their conversation from a
// source provider to a destination. The source is the single authority: no
// write ever flows from the destination back to the source.
package prsync

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// zwj is a zero width joiner. Inserted after an at sign or before the digits
// of an issue reference, it leaves the text looking identical while stopping
// the destination from resolving it into a mention or a cross link.
const zwj = "‍"

// Marker is the machine readable line SyncerD writes into every body and
// comment it creates. It is how a later run recognises its own writes, so an
// update rewrites in place rather than stacking a second header, and how a
// destination pull request whose number was lost from state is re-identified
// rather than duplicated.
func Marker(sourceRepo string, number int) string {
	return fmt.Sprintf("<!-- syncerd:pr:%s#%d -->", sourceRepo, number)
}

// CommentMarker identifies one mirrored comment. The source comment id is
// carried so a later run can find the comment it wrote for a given source
// comment even when state has been lost.
func CommentMarker(sourceRepo string, number int, sourceID string) string {
	return fmt.Sprintf("<!-- syncerd:comment:%s#%d:%s -->", sourceRepo, number, sourceID)
}

// HasMarker reports whether a body already carries the given marker, which
// means SyncerD wrote it.
func HasMarker(body, marker string) bool {
	return strings.Contains(body, marker)
}

// mentionRe matches an at sign mention. The leading group keeps the match
// from firing inside an email address, where the at sign is preceded by a
// word character.
var mentionRe = regexp.MustCompile(`(^|[^\w/@.-])@([A-Za-z0-9][A-Za-z0-9-]*)`)

// issueRefRe matches a bare issue or pull request reference such as #123.
var issueRefRe = regexp.MustCompile(`(^|[^\w&])#(\d+)`)

// Neutralise stops mirrored text from notifying or cross linking anyone at
// the destination.
//
// A body containing "@someone" posted verbatim would notify whichever
// destination account owns that handle: a different person, or nobody, and
// either way a notification the source author never sent. "#123" would cross
// link to an unrelated destination issue. Both are rewritten with a zero
// width joiner, so the text renders the same and resolves to nothing.
//
// Fenced code blocks are left exactly as they are. A mention inside a code
// fence does not notify anyone in the first place, and rewriting it would
// corrupt sample code, which is the one place in a pull request body where
// the characters have to survive byte for byte.
func Neutralise(text string) string {
	var out strings.Builder
	inFence := false

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out.WriteString(line)
		} else if inFence {
			out.WriteString(line)
		} else {
			out.WriteString(neutraliseLine(line))
		}
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// neutraliseLine rewrites mentions and issue references outside code fences.
// Inline code spans are handled the same as ordinary text: a mention inside
// backticks does not notify anyone, but neither does the zero width joiner
// change how it reads.
func neutraliseLine(line string) string {
	line = mentionRe.ReplaceAllString(line, "${1}@"+zwj+"${2}")
	return issueRefRe.ReplaceAllString(line, "${1}#"+zwj+"${2}")
}

// ComposeBody builds the body of a mirrored pull request: the marker, an
// attribution header naming the real author and linking to the source, then
// the source body with mentions neutralised.
//
// Composing an already composed body returns it unchanged, so an update that
// re-renders does not stack a second header.
func ComposeBody(pr vcs.PullRequest, sourceRepo string) string {
	marker := Marker(sourceRepo, pr.Number)
	body := stripHeader(pr.Body, marker)

	var header strings.Builder
	header.WriteString(marker)
	header.WriteString("\n")
	if pr.WebURL != "" {
		header.WriteString(fmt.Sprintf("> Mirrored from %s\n", pr.WebURL))
	} else {
		header.WriteString(fmt.Sprintf("> Mirrored from %s#%d\n", sourceRepo, pr.Number))
	}
	header.WriteString(fmt.Sprintf("> Opened by %s%s", attribution(pr.Author), openedOn(pr)))
	header.WriteString(". Replies here do not reach the author.\n\n")

	return header.String() + Neutralise(body)
}

// stripHeader removes a header SyncerD wrote earlier, so re-rendering an
// already mirrored body replaces the header rather than stacking a second
// one. A run that re-reads its own output, which happens whenever the
// destination body is fed back through composition, must converge.
func stripHeader(body, marker string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return body
	}
	rest := body[i+len(marker):]
	// The header ends at the first blank line after the marker.
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		return body[:i] + rest[j+2:]
	}
	return body[:i]
}

// ComposeComment builds the body of a mirrored discussion comment.
func ComposeComment(c vcs.Comment, sourceRepo string, number int) string {
	return fmt.Sprintf("%s\n> %s commented on %s (mirrored).\n\n%s",
		CommentMarker(sourceRepo, number, c.SourceID),
		attribution(c.Author),
		c.CreatedAt.UTC().Format("2006-01-02"),
		Neutralise(c.Body))
}

// ComposeReview builds the body of a mirrored review verdict.
//
// A verdict is mirrored as text, never submitted as a review at the
// destination. A bot approving a pull request would claim a review that
// nobody performed, and on a destination with required approvals it would
// satisfy a branch protection rule no human satisfied.
func ComposeReview(r vcs.Review, sourceRepo string, number int) string {
	body := fmt.Sprintf("%s\n> %s left a review at the source: **%s** on %s (mirrored, not an approval here).",
		CommentMarker(sourceRepo, number, "review-"+r.SourceID),
		attribution(r.Author),
		verdictLabel(r.State),
		r.SubmittedAt.UTC().Format("2006-01-02"))
	if strings.TrimSpace(r.Body) != "" {
		body += "\n\n" + Neutralise(r.Body)
	}
	return body
}

// verdictLabel renders a review state for a human reader.
func verdictLabel(state string) string {
	switch state {
	case "approved":
		return "approved"
	case "changes_requested":
		return "changes requested"
	case "commented":
		return "commented"
	default:
		if state == "" {
			return "reviewed"
		}
		return state
	}
}

// attribution renders an author without turning the handle into a live
// mention at the destination.
func attribution(a vcs.Actor) string {
	name := a.Handle
	if name == "" {
		name = a.DisplayName
	}
	if name == "" {
		return "an unknown author"
	}
	return "@" + zwj + name
}

// openedOn renders the creation date, or nothing when the provider did not
// report one.
func openedOn(pr vcs.PullRequest) string {
	if pr.CreatedAt.IsZero() {
		return ""
	}
	return " on " + pr.CreatedAt.UTC().Format("2006-01-02")
}
