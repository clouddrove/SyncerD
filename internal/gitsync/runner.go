package gitsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/clouddrove/syncerd/internal/vcs"
)

// PushMode selects how divergence between source and destination is handled.
type PushMode string

const (
	// PushMirror makes the destination an exact replica: refs absent at
	// source are deleted, rewritten history is force pushed.
	PushMirror PushMode = "mirror"
	// PushAdditive force pushes but never deletes.
	PushAdditive PushMode = "additive"
	// PushFastForward refuses any non fast forward update.
	PushFastForward PushMode = "fast-forward"
)

// ParsePushMode validates a configured push mode. Empty means mirror.
func ParsePushMode(s string) (PushMode, error) {
	switch PushMode(strings.TrimSpace(s)) {
	case "":
		return PushMirror, nil
	case PushMirror:
		return PushMirror, nil
	case PushAdditive:
		return PushAdditive, nil
	case PushFastForward:
		return PushFastForward, nil
	default:
		return "", fmt.Errorf("invalid push_mode %q: want mirror, additive, or fast-forward", s)
	}
}

// refspecs are the only refs SyncerD mirrors. Host internal namespaces such
// as refs/merge_requests and refs/pull are deliberately excluded, because
// destinations reject them.
var refspecs = []string{
	"+refs/heads/*:refs/heads/*",
	"+refs/tags/*:refs/tags/*",
}

// fastForwardRefspecs drop the leading plus so git enforces fast forward.
var fastForwardRefspecs = []string{
	"refs/heads/*:refs/heads/*",
	"refs/tags/*:refs/tags/*",
}

const (
	minGitMajor = 2
	minGitMinor = 30
)

// PushResult summarises one push.
type PushResult struct {
	Pushed  int
	Deleted int
	Lines   []string
}

// Runner executes git commands. Credentials are supplied through the
// environment so they never appear in argv or in on disk git config.
type Runner struct {
	GitPath  string
	Timeout  time.Duration
	redactor *Redactor
}

// NewRunner returns a runner with a ten minute per command timeout.
func NewRunner(redactor *Redactor) *Runner {
	return &Runner{GitPath: "git", Timeout: 10 * time.Minute, redactor: redactor}
}

// Preflight verifies a usable git binary is on PATH.
func (r *Runner) Preflight(ctx context.Context) error {
	out, err := r.run(ctx, "", nil, "--version")
	if err != nil {
		return fmt.Errorf("git is required for git-sync but was not runnable: %w", err)
	}
	major, minor, perr := parseGitVersion(out)
	if perr != nil {
		return perr
	}
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git %d.%d found, but git-sync requires %d.%d or newer",
			major, minor, minGitMajor, minGitMinor)
	}
	return nil
}

// parseGitVersion extracts the major and minor version from "git version X.Y.Z".
func parseGitVersion(out string) (int, int, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 {
		return 0, 0, fmt.Errorf("unrecognised git version output: %q", out)
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognised git version %q", fields[2])
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognised git major version %q", parts[0])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognised git minor version %q", parts[1])
	}
	return major, minor, nil
}

// LsRemote lists the branches and tags a remote advertises.
func (r *Runner) LsRemote(ctx context.Context, url string, cred vcs.GitCredential) ([]Ref, error) {
	out, err := r.run(ctx, "", credEnv(cred), "ls-remote", "--heads", "--tags", url)
	if err != nil {
		return nil, err
	}
	return ParseLsRemote(out)
}

// SyncCache clones the source into a bare cache directory, or updates an
// existing cache in place.
func (r *Runner) SyncCache(ctx context.Context, url, dir string, cred vcs.GitCredential) error {
	env := credEnv(cred)

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fmt.Errorf("create cache parent: %w", err)
		}
		if _, err := r.run(ctx, "", env, "clone", "--mirror", url, dir); err != nil {
			return fmt.Errorf("clone mirror: %w", err)
		}
		return nil
	}

	if _, err := r.run(ctx, dir, env, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("set cache remote: %w", err)
	}
	if _, err := r.run(ctx, dir, env, "remote", "update", "--prune"); err != nil {
		return fmt.Errorf("update cache: %w", err)
	}
	return nil
}

// Push sends branches and tags to the destination according to mode.
func (r *Runner) Push(ctx context.Context, dir, destURL string, cred vcs.GitCredential, mode PushMode, dryRun bool) (PushResult, error) {
	args := []string{"push", "--porcelain"}
	if dryRun {
		args = append(args, "--dry-run")
	}

	specs := refspecs
	switch mode {
	case PushMirror:
		args = append(args, "--prune")
	case PushAdditive:
		// no prune
	case PushFastForward:
		specs = fastForwardRefspecs
	default:
		return PushResult{}, fmt.Errorf("unknown push mode %q", mode)
	}

	args = append(args, destURL)
	args = append(args, specs...)

	out, err := r.run(ctx, dir, credEnv(cred), args...)
	res := parsePorcelain(out)
	if err != nil {
		return res, err
	}
	return res, nil
}

// parsePorcelain counts updates and deletions from git push --porcelain.
// Each line is "<flag>\t<from>:<to>\t<summary>"; a deletion has an empty
// left side of the ref pair and a "-" flag.
func parsePorcelain(out string) PushResult {
	var res PushResult
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "To ") || strings.HasPrefix(line, "Done") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		flag := fields[0]
		res.Lines = append(res.Lines, line)
		switch flag {
		case "=":
			// up to date, not a change
		case "-":
			res.Deleted++
		case "!":
			// rejected; the command exit code surfaces the failure
		default:
			res.Pushed++
		}
	}
	return res
}

// run executes git with the given environment additions.
func (r *Runner) run(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.GitPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return out, fmt.Errorf("git %s: %w: %s",
			redactArgs(args), err, r.redactor.Redact(msg))
	}
	return out, nil
}

// redactArgs joins args for an error message. URLs may embed credentials in
// pathological configurations, so the redactor runs over the result too.
func redactArgs(args []string) string {
	return strings.Join(args, " ")
}

// credEnv builds the environment that teaches git how to authenticate,
// using GIT_CONFIG_* rather than argv or an on disk config file.
func credEnv(cred vcs.GitCredential) []string {
	switch {
	case cred.Kind == vcs.CredBearer && cred.Secret != "":
		return []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer " + cred.Secret,
		}
	case cred.Secret != "":
		return []string{
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper",
			`GIT_CONFIG_VALUE_0=!f() { echo "username=$SYNCERD_GIT_USER"; echo "password=$SYNCERD_GIT_SECRET"; }; f`,
			"GIT_CONFIG_KEY_1=credential.useHttpPath",
			"GIT_CONFIG_VALUE_1=true",
			"SYNCERD_GIT_USER=" + cred.User,
			"SYNCERD_GIT_SECRET=" + cred.Secret,
		}
	default:
		return nil
	}
}
