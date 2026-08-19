package prsync

import (
	"context"
	"fmt"

	"github.com/clouddrove/syncerd/internal/logging"
	"github.com/clouddrove/syncerd/internal/state"
	"github.com/clouddrove/syncerd/internal/vcs"
)

// Options is everything one repository's pull request mirroring needs.
type Options struct {
	Mirror       string
	SourceRepo   string // owner/name at the source, the state key
	DestRepo     string // repository path at the destination
	BranchPrefix string

	Source vcs.PullRequestLister
	Dest   vcs.PullRequestWriter

	// SourceConv and DestConv are set only when conversation mirroring is
	// on. Either being nil disables comments and reviews for this run.
	SourceConv vcs.PullRequestConversation
	DestConv   vcs.PullRequestConversation

	Comments bool
	Reviews  bool
	Labels   bool

	State  *state.GitState
	DryRun bool

	// Redact strips secrets from anything bound for a log line or a
	// returned error. Optional; identity when nil.
	Redact func(string) string
}

// Result counts what a run did, for the run report.
type Result struct {
	Created         int
	Updated         int
	Closed          int
	CommentsCreated int
	Downgraded      int
	Failures        []string
}

// Sync reconciles the destination's pull requests with the source's.
//
// The source is the authority: every difference is resolved by making the
// destination match, and nothing is ever read from the destination and
// written back. One pull request failing does not stop the others, and the
// error returned describes the run rather than any single failure.
func Sync(ctx context.Context, prs []vcs.PullRequest, opts Options) (Result, error) {
	var res Result

	for _, pr := range prs {
		if ctx.Err() != nil {
			break
		}
		if err := syncOne(ctx, pr, opts, &res); err != nil {
			msg := fmt.Sprintf("pull request %d: %s", pr.Number, opts.redact(err.Error()))
			res.Failures = append(res.Failures, msg)
			logging.Warn(fmt.Sprintf("mirror %s: %s %s", opts.Mirror, opts.SourceRepo, msg),
				"mirror", opts.Mirror, "source", opts.SourceRepo, "pull_request", pr.Number)
		}
	}

	if len(res.Failures) > 0 {
		return res, fmt.Errorf("%d pull request(s) failed to mirror", len(res.Failures))
	}
	return res, nil
}

// syncOne applies the lifecycle rules to a single source pull request.
func syncOne(ctx context.Context, pr vcs.PullRequest, opts Options, res *Result) error {
	branch := vcs.PRBranch(opts.BranchPrefix, pr.Number)
	rec, hasRec := opts.State.GetPR(opts.Mirror, opts.SourceRepo, pr.Number)

	// Locate the destination pull request. State answers first; the
	// destination itself answers when state is missing or stale, which is
	// what keeps a lost state file from producing duplicates.
	destNumber := 0
	destState := vcs.PRState("")
	if hasRec && rec.DestNumber != 0 {
		destNumber = rec.DestNumber
		destState = vcs.PRState(rec.DestState)
	}

	// A recorded divergence is re-checked rather than trusted, because the
	// only way out of it is a human reopening the destination pull request,
	// and nothing else would ever notice that they had.
	knownDivergent := hasRec && rec.DestState == destStateDivergent

	if destNumber == 0 || knownDivergent {
		found, ok, err := opts.Dest.FindPullRequest(ctx, opts.DestRepo, branch)
		if err != nil {
			return fmt.Errorf("look up destination: %w", err)
		}
		if ok {
			destNumber = found.Number
			destState = found.State
		} else if knownDivergent {
			// The destination pull request is gone entirely. Forget the
			// divergence so the create path can run.
			destNumber = 0
			destState = ""
		}
	}

	// An untouched pull request whose destination is known and whose state
	// already agrees needs no further calls at all. This watermark is what
	// keeps a repository with many open pull requests cheap.
	//
	// A source that reports no update timestamp at all, which Azure DevOps
	// does, cannot be watermarked: skipping on a zero time would freeze the
	// mirror after its first run. Those pull requests are reconciled every
	// run instead, which is slower and correct.
	if hasRec && destNumber != 0 && !pr.UpdatedAt.IsZero() &&
		!pr.UpdatedAt.After(rec.SourceUpdated) &&
		statesAgree(pr.State, destState) {
		return nil
	}

	spec := vcs.PullRequestSpec{
		Title:      pr.Title,
		Body:       ComposeBody(pr, opts.SourceRepo),
		HeadBranch: branch,
		BaseBranch: pr.BaseBranch,
		Draft:      pr.Draft,
	}
	if opts.Labels {
		spec.Labels = pr.Labels
	}

	switch {
	case destNumber == 0:
		// Nothing at the destination. A closed source pull request that was
		// never mirrored stays unmirrored: creating it only to close it
		// would post a notification for work that finished before this
		// mirror existed.
		if pr.State != vcs.PROpen {
			return nil
		}
		if opts.DryRun {
			logging.Info(fmt.Sprintf("mirror %s: would create destination pull request for %s#%d", opts.Mirror, opts.SourceRepo, pr.Number))
			res.Created++
			return nil
		}
		created, err := opts.Dest.CreatePullRequest(ctx, opts.DestRepo, spec)
		if err != nil {
			return fmt.Errorf("create destination pull request: %w", err)
		}
		destNumber = created.Number
		destState = vcs.PROpen
		res.Created++

	default:
		if opts.DryRun {
			logging.Info(fmt.Sprintf("mirror %s: would update destination pull request %d for %s#%d", opts.Mirror, destNumber, opts.SourceRepo, pr.Number))
			res.Updated++
			break
		}

		// A destination someone closed by hand is reopened first: the
		// source is the authority on whether this work is still open.
		if pr.State == vcs.PROpen && destState != vcs.PROpen {
			reopener, canReopen := opts.Dest.(vcs.PullRequestReopener)
			if !canReopen {
				// Bitbucket has no reopen endpoint and CodeCommit forbids
				// the transition. Say so once and leave the pull request
				// closed: a second pull request for the same work is worse
				// than a closed one somebody can reopen by hand.
				//
				// Nothing further is attempted on it. Both of those
				// providers reject an update to a closed pull request, so
				// carrying on would turn an unfixable divergence into a
				// failure on every single run.
				if !knownDivergent {
					logging.Warn(fmt.Sprintf("mirror %s: %s#%d is open at the source but destination pull request %d is closed, and this destination cannot reopen; leaving it closed",
						opts.Mirror, opts.SourceRepo, pr.Number, destNumber),
						"mirror", opts.Mirror, "source", opts.SourceRepo, "pull_request", pr.Number)
				}
				if !opts.DryRun {
					rec.DestNumber = destNumber
					rec.DestState = destStateDivergent
					rec.SourceUpdated = pr.UpdatedAt
					opts.State.MarkPR(opts.Mirror, opts.SourceRepo, pr.Number, rec)
				}
				return nil
			}
			if err := reopener.ReopenPullRequest(ctx, opts.DestRepo, destNumber); err != nil {
				return fmt.Errorf("reopen destination pull request: %w", err)
			}
			destState = vcs.PROpen
		}

		// Only an open pull request can be updated. A destination already
		// closed because its source finished needs no rewrite, and asking
		// for one is an error on several providers.
		if destState == vcs.PROpen {
			if err := opts.Dest.UpdatePullRequest(ctx, opts.DestRepo, destNumber, spec); err != nil {
				return fmt.Errorf("update destination pull request: %w", err)
			}
			res.Updated++
		}
	}

	// Conversation before closing, so the closing note is the last thing on
	// a finished pull request.
	if opts.Comments || opts.Reviews {
		created, downgraded, err := syncConversation(ctx, pr, destNumber, opts, &rec)
		res.CommentsCreated += created
		res.Downgraded += downgraded
		if err != nil {
			return err
		}
	}

	if pr.State != vcs.PROpen && destState == vcs.PROpen {
		if err := closeDestination(ctx, pr, destNumber, opts); err != nil {
			return err
		}
		destState = pr.State
		res.Closed++
	}

	if !opts.DryRun {
		rec.DestNumber = destNumber
		rec.DestState = string(destState)
		rec.SourceUpdated = pr.UpdatedAt
		opts.State.MarkPR(opts.Mirror, opts.SourceRepo, pr.Number, rec)
	}
	return nil
}

// closeDestination closes a destination pull request whose source has
// finished, leaving a note that says what actually happened.
//
// A merged source is never merged here. The ref mirror pushes the source's
// own merge commit onto the destination's base branch, so a merge performed
// at the destination would be a second, different commit that the next
// mirror push overwrites. Closing with the merge SHA names a commit the
// destination genuinely has.
func closeDestination(ctx context.Context, pr vcs.PullRequest, destNumber int, opts Options) error {
	note := fmt.Sprintf("%s\nClosed because the source pull request was closed without merging.",
		Marker(opts.SourceRepo, pr.Number))
	if pr.State == vcs.PRMerged {
		note = fmt.Sprintf("%s\nClosed because the source pull request was merged%s. The merge commit reaches this repository through the branch mirror, so this pull request is not merged here.",
			Marker(opts.SourceRepo, pr.Number), mergedAs(pr))
	}

	if opts.DryRun {
		logging.Info(fmt.Sprintf("mirror %s: would close destination pull request %d for %s#%d", opts.Mirror, destNumber, opts.SourceRepo, pr.Number))
		return nil
	}

	if opts.DestConv != nil {
		if _, err := opts.DestConv.CreateComment(ctx, opts.DestRepo, destNumber, note); err != nil {
			// The note is explanatory. Failing to post it must not leave
			// the destination pull request open forever.
			logging.Warn(fmt.Sprintf("mirror %s: could not post the closing note on destination pull request %d: %s",
				opts.Mirror, destNumber, opts.redact(err.Error())))
		}
	}

	// Closed, never merged, and PRClosed rather than pr.State so the
	// request that reaches a provider cannot be read as "merge this". A
	// provider is free to map PRMerged however it likes; this caller never
	// gives it the chance.
	if err := opts.Dest.ClosePullRequest(ctx, opts.DestRepo, destNumber); err != nil {
		return fmt.Errorf("close destination pull request: %w", err)
	}
	return nil
}

// mergedAs renders the source merge commit when the provider reported one.
func mergedAs(pr vcs.PullRequest) string {
	if pr.MergeSHA == "" {
		return ""
	}
	return " as " + pr.MergeSHA
}

// destStateDivergent marks a destination that is closed while its source is
// open, on a provider that cannot reopen.
//
// It is recorded so the warning is not repeated on every run, and re-checked
// on every run so a destination somebody reopened by hand is picked back up.
const destStateDivergent = "closed-divergent"

// statesAgree reports whether the destination already reflects the source.
//
// Merged and closed are the same thing at the destination, since a merged
// source closes rather than merges.
func statesAgree(source, dest vcs.PRState) bool {
	if source == vcs.PROpen {
		return dest == vcs.PROpen
	}
	return dest == vcs.PRClosed || dest == vcs.PRMerged
}

// redact applies the configured redactor, or returns the string unchanged.
func (o Options) redact(s string) string {
	if o.Redact == nil {
		return s
	}
	return o.Redact(s)
}
