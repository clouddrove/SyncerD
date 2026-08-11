package gitsync

import (
	"context"
	"fmt"
	"net/url"
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
	if err := rejectURLCredentials(url); err != nil {
		return nil, err
	}
	if err := validateCredential(cred); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "", credEnv(cred, url), "ls-remote", "--heads", "--tags", url)
	if err != nil {
		return nil, err
	}
	return ParseLsRemote(out)
}

// SyncCache clones the source into a bare cache directory, or updates an
// existing cache in place. The cache is built and kept explicitly scoped to
// branches and tags: a plain clone --mirror would also fetch host internal
// namespaces such as refs/pull and refs/merge_requests, which the global
// constraints forbid syncing.
func (r *Runner) SyncCache(ctx context.Context, url, dir string, cred vcs.GitCredential) error {
	if err := rejectURLCredentials(url); err != nil {
		return err
	}
	if err := validateCredential(cred); err != nil {
		return err
	}
	env := credEnv(cred, url)

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fmt.Errorf("create cache parent: %w", err)
		}
		if _, err := r.run(ctx, "", env, "init", "--bare", dir); err != nil {
			return fmt.Errorf("init cache: %w", err)
		}
		if _, err := r.run(ctx, dir, env, "remote", "add", "origin", url); err != nil {
			return fmt.Errorf("add cache remote: %w", err)
		}
		if err := r.setCacheRefspecs(ctx, dir, env); err != nil {
			return err
		}
	} else {
		if _, err := r.run(ctx, dir, env, "remote", "set-url", "origin", url); err != nil {
			return fmt.Errorf("set cache remote: %w", err)
		}
		// Re-apply on every run so a cache built by an older version, or
		// one left with clone --mirror defaults, is corrected in place.
		if err := r.setCacheRefspecs(ctx, dir, env); err != nil {
			return err
		}
	}

	if _, err := r.run(ctx, dir, env, "fetch", "--prune", "--no-tags", "origin"); err != nil {
		return fmt.Errorf("update cache: %w", err)
	}
	return nil
}

// setCacheRefspecs pins the cache to branches and tags only. Host internal
// namespaces such as refs/pull and refs/merge_requests are never fetched,
// which keeps the cache small and keeps a later push from carrying refs the
// destination would reject.
func (r *Runner) setCacheRefspecs(ctx context.Context, dir string, env []string) error {
	if _, err := r.run(ctx, dir, env, "config", "--unset-all", "remote.origin.mirror"); err != nil {
		// A cache that never had mirror set returns exit 5. Not an error.
		_ = err
	}
	if _, err := r.run(ctx, dir, env, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
		return fmt.Errorf("set cache fetch refspec: %w", err)
	}
	if _, err := r.run(ctx, dir, env, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*"); err != nil {
		return fmt.Errorf("add cache tag refspec: %w", err)
	}
	return nil
}

// Push sends branches and tags to the destination according to mode.
func (r *Runner) Push(ctx context.Context, dir, destURL string, cred vcs.GitCredential, mode PushMode, dryRun bool) (PushResult, error) {
	if err := rejectURLCredentials(destURL); err != nil {
		return PushResult{}, err
	}
	if err := validateCredential(cred); err != nil {
		return PushResult{}, err
	}

	// Refuse to prune from an empty cache. A cache with no refs would
	// delete every matching ref at the destination, and on a host that
	// permits those deletions git exits zero, so the wipe would be
	// reported as a successful sync.
	if mode == PushMirror {
		local, err := r.LsRemote(ctx, dir, vcs.GitCredential{})
		if err != nil {
			return PushResult{}, fmt.Errorf("inspect cache before mirror push: %w", err)
		}
		if len(local) == 0 {
			return PushResult{}, fmt.Errorf("refusing a mirror push from an empty cache at %q: pruning would delete every branch and tag at the destination", dir)
		}
	}

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

	out, err := r.run(ctx, dir, credEnv(cred, destURL), args...)
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
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gitPath := r.GitPath
	if gitPath == "" {
		gitPath = "git"
	}

	cmd := exec.CommandContext(ctx, gitPath, args...)
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
			r.redactor.Redact(strings.Join(args, " ")), err, r.redactor.Redact(msg))
	}
	return out, nil
}

// rejectURLCredentials refuses a URL with embedded userinfo. Credentials
// belong in the environment, not in a URL that git writes to the cache
// config and echoes in its own error output.
func rejectURLCredentials(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// Not a URL git would parse as HTTP either. Let git report it.
		return nil
	}
	if u.User != nil {
		return fmt.Errorf("refusing a remote URL with embedded credentials; supply them through the provider configuration instead")
	}
	return nil
}

// validateCredential rejects a secret that would corrupt the credential
// protocol. Git reads the helper output line by line, so an embedded
// newline silently truncates the value and turns the remainder into a
// separate directive.
func validateCredential(cred vcs.GitCredential) error {
	if strings.ContainsAny(cred.Secret, "\n\r") {
		return fmt.Errorf("credential secret contains a newline, which git would read as a protocol directive")
	}
	if strings.ContainsAny(cred.User, "\n\r") {
		return fmt.Errorf("credential username contains a newline, which git would read as a protocol directive")
	}
	return nil
}

// credEnv builds the environment that teaches git how to authenticate,
// using GIT_CONFIG_* rather than argv or an on disk config file. The
// inherited helper chain is reset first so a global credential helper
// cannot answer ahead of us, and so git does not hand the token to an OS
// keychain or to ~/.git-credentials when the push succeeds.
func credEnv(cred vcs.GitCredential, remoteURL string) []string {
	env := []string{"GIT_CONFIG_NOSYSTEM=1"}

	switch {
	case cred.Kind == vcs.CredBearer && cred.Secret != "":
		key := "http.extraHeader"
		if base := urlConfigBase(remoteURL); base != "" {
			key = "http." + base + ".extraHeader"
		}
		return append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0="+key,
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+cred.Secret,
		)

	case cred.Secret != "":
		return append(env,
			"GIT_CONFIG_COUNT=3",
			// An empty helper resets the inherited chain.
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0=",
			"GIT_CONFIG_KEY_1=credential.helper",
			`GIT_CONFIG_VALUE_1=!f() { printf '%s\n' "username=$SYNCERD_GIT_USER" "password=$SYNCERD_GIT_SECRET"; }; f`,
			"GIT_CONFIG_KEY_2=credential.useHttpPath",
			"GIT_CONFIG_VALUE_2=true",
			"SYNCERD_GIT_USER="+cred.User,
			"SYNCERD_GIT_SECRET="+cred.Secret,
		)

	default:
		// Clear any inherited config block so a parent process cannot
		// inject configuration into our git invocations.
		return append(env, "GIT_CONFIG_COUNT=0")
	}
}

// urlConfigBase returns the scheme, host, and port prefix used to scope
// http.<base>.* configuration, or the empty string when the URL is not a
// parseable HTTP URL.
func urlConfigBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}
