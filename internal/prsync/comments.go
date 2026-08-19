package prsync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/clouddrove/syncerd/internal/logging"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
)

// syncConversation mirrors the discussion, the inline review comments, and
// the review verdicts of one pull request.
//
// Only comments SyncerD itself wrote are ever updated or deleted, tracked
// through rec.CommentIDs. The source is the authority over what it
// published, not over what somebody said at the destination, so a comment
// written by a person there is left alone even when the source has nothing
// corresponding to it.
func syncConversation(ctx context.Context, pr vcs.PullRequest, destNumber int, opts Options, rec *state.PRRecord) (created int, downgraded int, err error) {
	if opts.SourceConv == nil || opts.DestConv == nil || destNumber == 0 {
		return 0, 0, nil
	}
	if rec.CommentIDs == nil {
		rec.CommentIDs = make(map[string]string)
	}

	seen := make(map[string]bool)

	if opts.Comments {
		comments, lerr := opts.SourceConv.ListComments(ctx, opts.SourceRepo, pr.Number)
		if lerr != nil {
			return created, downgraded, fmt.Errorf("list source comments: %w", lerr)
		}
		for _, c := range comments {
			seen[c.SourceID] = true
			body := ComposeComment(c, opts.SourceRepo, pr.Number)
			n, derr := upsert(ctx, opts, destNumber, rec, c.SourceID, body)
			created += n
			if derr != nil {
				return created, downgraded, derr
			}
		}

		reviewComments, rerr := opts.SourceConv.ListReviewComments(ctx, opts.SourceRepo, pr.Number)
		if rerr != nil {
			return created, downgraded, fmt.Errorf("list source review comments: %w", rerr)
		}
		for _, rc := range reviewComments {
			seen[rc.SourceID] = true
			n, d, cerr := upsertAnchored(ctx, opts, destNumber, rec, pr.Number, rc)
			created += n
			downgraded += d
			if cerr != nil {
				return created, downgraded, cerr
			}
		}
	}

	if opts.Reviews {
		reviews, rerr := opts.SourceConv.ListReviews(ctx, opts.SourceRepo, pr.Number)
		if rerr != nil {
			return created, downgraded, fmt.Errorf("list source reviews: %w", rerr)
		}
		for _, r := range reviews {
			// A verdict is mirrored as text under its own key. It is never
			// submitted as a review at the destination: a bot approval
			// would satisfy branch protection that no human satisfied.
			key := "review-" + r.SourceID
			seen[key] = true
			body := ComposeReview(r, opts.SourceRepo, pr.Number)
			n, derr := upsert(ctx, opts, destNumber, rec, key, body)
			created += n
			if derr != nil {
				return created, downgraded, derr
			}
		}
	}

	// A source comment that has since been deleted takes its mirrored copy
	// with it, but only the copy SyncerD wrote, and only in a category this
	// run actually listed.
	//
	// That second condition matters: without it, turning comments off would
	// delete every comment previously mirrored, and turning reviews off
	// would delete every mirrored verdict, because nothing in those
	// categories would appear in seen.
	for sourceID, destID := range rec.CommentIDs {
		if seen[sourceID] {
			continue
		}
		if isReviewKey(sourceID) && !opts.Reviews {
			continue
		}
		if !isReviewKey(sourceID) && !opts.Comments {
			continue
		}
		if opts.DryRun {
			logging.Info(fmt.Sprintf("mirror %s: would delete mirrored comment %s on destination pull request %d", opts.Mirror, destID, destNumber))
			continue
		}
		if derr := opts.DestConv.DeleteComment(ctx, opts.DestRepo, destID); derr != nil {
			logging.Warn(fmt.Sprintf("mirror %s: could not delete mirrored comment %s: %s", opts.Mirror, destID, opts.redact(derr.Error())))
			continue
		}
		delete(rec.CommentIDs, sourceID)
	}

	return created, downgraded, nil
}

// isReviewKey reports whether a tracked id belongs to a mirrored review
// verdict rather than a comment. ComposeReview prefixes those keys.
func isReviewKey(sourceID string) bool {
	return strings.HasPrefix(sourceID, "review-")
}

// upsert posts a comment the first time and rewrites it after that.
func upsert(ctx context.Context, opts Options, destNumber int, rec *state.PRRecord, sourceID, body string) (int, error) {
	if destID, ok := rec.CommentIDs[sourceID]; ok {
		if opts.DryRun {
			return 0, nil
		}
		if err := opts.DestConv.UpdateComment(ctx, opts.DestRepo, destID, body); err != nil {
			return 0, fmt.Errorf("update mirrored comment %s: %w", destID, err)
		}
		return 0, nil
	}

	if opts.DryRun {
		logging.Info(fmt.Sprintf("mirror %s: would mirror comment %s to destination pull request %d", opts.Mirror, sourceID, destNumber))
		return 1, nil
	}

	destID, err := opts.DestConv.CreateComment(ctx, opts.DestRepo, destNumber, body)
	if err != nil {
		return 0, fmt.Errorf("create mirrored comment: %w", err)
	}
	rec.CommentIDs[sourceID] = destID
	return 1, nil
}

// upsertAnchored posts an inline review comment, falling back to a
// discussion comment when the destination refuses the anchor.
//
// The anchor usually holds, because the commit it names reaches the
// destination through the branch mirror. It fails when the line sits
// outside the destination pull request's diff, and a remark that cannot be
// pinned to a line is still worth carrying, so it is downgraded rather than
// dropped, with the file and line named in the text.
func upsertAnchored(ctx context.Context, opts Options, destNumber int, rec *state.PRRecord, sourceNumber int, rc vcs.ReviewComment) (created int, downgraded int, err error) {
	body := ComposeComment(rc.Comment, opts.SourceRepo, sourceNumber)

	if destID, ok := rec.CommentIDs[rc.SourceID]; ok {
		if opts.DryRun {
			return 0, 0, nil
		}
		// The provider decides how to address the comment: an inline
		// comment and a downgraded one can live in different id spaces,
		// which is why the id it minted is handed straight back.
		if uerr := opts.DestConv.UpdateComment(ctx, opts.DestRepo, destID, body); uerr != nil {
			return 0, 0, fmt.Errorf("update mirrored review comment %s: %w", destID, uerr)
		}
		return 0, 0, nil
	}

	if opts.DryRun {
		logging.Info(fmt.Sprintf("mirror %s: would mirror review comment %s to destination pull request %d", opts.Mirror, rc.SourceID, destNumber))
		return 1, 0, nil
	}

	anchored := rc
	anchored.Body = body
	destID, cerr := opts.DestConv.CreateReviewComment(ctx, opts.DestRepo, destNumber, anchored)
	if cerr == nil {
		rec.CommentIDs[rc.SourceID] = destID
		return 1, 0, nil
	}
	if !errors.Is(cerr, vcs.ErrAnchorRejected) {
		return 0, 0, fmt.Errorf("create mirrored review comment: %w", cerr)
	}

	fallback := fmt.Sprintf("%s\n> On `%s:%d`, which is outside this pull request's diff here.\n\n%s",
		CommentMarker(opts.SourceRepo, sourceNumber, rc.SourceID), rc.Path, rc.Line, Neutralise(rc.Body))

	destID, ferr := opts.DestConv.CreateComment(ctx, opts.DestRepo, destNumber, fallback)
	if ferr != nil {
		return 0, 0, fmt.Errorf("create downgraded review comment: %w", ferr)
	}
	rec.CommentIDs[rc.SourceID] = destID
	logging.Info(fmt.Sprintf("mirror %s: review comment on %s:%d could not be anchored at the destination and was posted as a discussion comment",
		opts.Mirror, rc.Path, rc.Line))
	return 1, 1, nil
}
