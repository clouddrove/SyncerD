# git-sync Local Test Runbook

How to exercise `syncerd git-sync` on your own machine, from a build with no
credentials through a real GitHub to GitLab mirror and back.

Every command here has been run against this branch. Expected output is quoted
verbatim where it matters.

**Read the safety note before Phase 4.** `git-sync` force pushes and prunes refs
at the destination. Use throwaway repositories.

## Contents

| Phase | What it proves | Credentials |
|---|---|---|
| 0 | The tool builds on every release target | none |
| 1 | Unit and integration suites pass | none |
| 2 | The CLI surface and config validation behave | none |
| 3 | Discovery and filtering work against a real org | read only |
| 4 | A real mirror lands | read and write |
| 5 | Unchanged repos are skipped | read and write |
| 6 | A new branch is picked up | read and write |
| 7 | A deleted branch is pruned | read and write |
| 8 | The adopt guard refuses an unknown destination | read and write |
| 9 | The reverse direction works | read and write |
| 10 | Slack reports correctly | read and write |
| 11 | Scheduled mode runs | read and write |

---

## Prerequisites

```bash
go version    # 1.25 or newer
git --version # 2.30 or newer, git-sync refuses below this
```

`git` must be on `PATH`. `git-sync` shells out to it; there is no pure-Go
fallback. The published Docker image now installs git, but a local build is
still the fastest way to iterate through this runbook.

---

## Phase 0: Build

```bash
cd path/to/syncerd

go build -o ./syncerd .
./syncerd --version

# Put the binary on PATH so later phases, which cd into a scratch
# directory, can call it as plain `syncerd`.
export PATH="$PWD:$PATH"
```

Confirm all three release targets compile:

```bash
for os in linux darwin windows; do
  GOOS=$os GOARCH=amd64 go build -o /dev/null ./... && echo "$os ok"
done
```

Expect `linux ok`, `darwin ok`, `windows ok`.

---

## Phase 1: Test suites, no credentials

```bash
go test ./...
go test ./... -race
go vet ./...
gofmt -l ./internal ./main.go   # must print nothing
```

The `internal/gitsync` suite creates real bare repositories under `t.TempDir()`
and drives the actual `git` binary, so it covers clone, fetch, push, prune,
force-push refusal, and the empty-cache guard without touching the network.

Run just the mirror mechanics if you want to watch them:

```bash
go test ./internal/gitsync/ -run 'TestPush|TestSyncCache|TestEngine' -v
```

---

## Phase 2: CLI surface and config validation, no credentials

Verify the existing Docker image sync is untouched:

```bash
./syncerd sync --once --config /dev/null
```

Expected, byte for byte, unchanged from before this feature:

```
at least one destination is required
```

Verify git-sync is inert without config:

```bash
./syncerd git-sync --once --config /dev/null
```

```
git configuration is required for git-sync
```

Now check the validator catches the common mistakes. Each of these fails at
config load, before any network call.

```bash
mkdir -p /tmp/gitsync-rb && cd /tmp/gitsync-rb
```

**Missing token:**

```bash
cat > 01-no-token.yaml <<'EOF'
git:
  providers:
    - name: gh
      type: github
      owner: acme
  mirrors:
    - name: gh-to-gl
      source: gh
      destination: gh
EOF
syncerd git-sync --once --config 01-no-token.yaml
```

```
git.providers[0].token is required for type "github"; set it in the config or as SYNCERD_GIT_GH_TOKEN
```

**Bitbucket without an email** (app passwords were retired 2026-07-28, so API
tokens authenticate with the account email):

```bash
cat > 02-bitbucket.yaml <<'EOF'
git:
  providers:
    - name: gh
      type: github
      owner: acme
      token: ghp_placeholder
    - name: bb
      type: bitbucket
      owner: workspace
      token: tok_placeholder
  mirrors:
    - name: gh-to-bb
      source: gh
      destination: bb
EOF
syncerd git-sync --once --config 02-bitbucket.yaml
```

```
git.providers[1].email is required for bitbucket: app passwords were retired on 2026-07-28 and API tokens authenticate with the account email
```

**A nested name template aimed at GitHub.** GitHub repository names cannot
contain a slash, so this is refused at startup rather than at push time:

```bash
cat > 03-nested.yaml <<'EOF'
git:
  providers:
    - name: gl
      type: gitlab
      owner: mygroup
      token: glpat_placeholder
    - name: gh
      type: github
      owner: acme
      token: ghp_placeholder
  mirrors:
    - name: gl-to-gh
      source: gl
      destination: gh
      name_template: "{{ .Owner }}/{{ .Repo }}"
EOF
syncerd git-sync --once --config 03-nested.yaml
```

```
failed to create git syncer: mirror "gl-to-gh": name_template renders a nested name but destination "gh" does not support nested paths
```

**Observability flags** need no credentials either and can be checked here:
`--report <path>` (a JSON run summary), `--metrics-file <path>` (Prometheus
textfile collector metrics), and `--log-format json` (structured logging;
text stays the default). All three are opt in, all three are shared with
`sync`, and none change what a run does. See
[Observability](../README.md#observability) in the README for the alert
query and the one-file-per-command note.

---

## Phase 3 onward: credentials

### Tokens

| Provider | Token | Scopes |
|---|---|---|
| GitHub | fine-grained PAT | Metadata read, Contents read, Administration write (only if `create_missing`); a fine grained token must also grant access to each private repository you expect to be mirrored, or discovery reports fewer repositories than the source holds |
| GitLab | group access token | `api` to create projects, plus `write_repository` |

Environment variable names derive from the **provider name** in your config,
upper cased with non alphanumerics mapped to underscore:

```bash
export SYNCERD_GIT_GH_TOKEN='ghp_your_real_token'
export SYNCERD_GIT_GL_TOKEN='glpat-your_real_token'
```

A provider named `gh-mirrors` would read `SYNCERD_GIT_GH_MIRRORS_TOKEN`. Two
provider names that normalise to the same variable are rejected at config load.

Values written in the config file **win** over the environment. That is
deliberate, so a deployment can pin a secret, but it means a stray `token:` in
the file silently shadows your exported variable.

### Working config

```bash
cd /tmp/gitsync-rb
cat > mirror.yaml <<'EOF'
git:
  providers:
    - name: gh
      type: github
      owner: YOUR_GITHUB_ORG_OR_USER
    - name: gl
      type: gitlab
      owner: YOUR_GITLAB_GROUP

  mirrors:
    - name: gh-to-gl
      source: gh
      destination: gl
      filters:
        include: ["syncerd-rbtest-*"]
        skip_archived: true
        skip_forks: true
      create_missing: true
      visibility: private

  # Keep both under the test directory. See "state_path" under Gotchas.
  work_dir: /tmp/gitsync-rb/cache
  state_path: /tmp/gitsync-rb/cache/git-state.json
  concurrency: 2
EOF
```

Create two throwaway repos in your GitHub org named `syncerd-rbtest-one` and
`syncerd-rbtest-two`, each with at least one commit and one tag. The `include`
glob keeps everything else in the org out of scope.

---

## Phase 3: Dry run, read only

```bash
syncerd git-sync --dry-run --config mirror.yaml
```

`--dry-run` implies `--once`. It never creates a destination repository, never
pushes, never writes state, and never posts to Slack.

Look for, per repository:

```
mirror gh-to-gl: 2 repositories discovered, 2 selected
mirror gh-to-gl: would create destination syncerd-rbtest-one
mirror gh-to-gl: acme/syncerd-rbtest-one -> syncerd-rbtest-one would apply N ref change(s):
  ...one line per ref...
```

Those per-ref lines are the point of the dry run. On a destination that already
exists, deletions appear with a leading `-`, which is your last chance to catch
a mirror pointed somewhere wrong.

Confirm nothing was written:

```bash
ls /tmp/gitsync-rb/cache/git-state.json 2>&1   # expect: No such file or directory
```

Then check your GitLab group in a browser: no projects should have appeared.

---

## Phase 4: First real mirror

> **Safety.** From here the tool writes to the destination. `push_mode` defaults
> to `mirror`, which deletes destination branches and tags absent at source and
> force pushes rewritten history. Point it only at a group you are willing to
> lose. The adopt guard (Phase 8) protects a destination that already has
> content, but nothing protects you from choosing the wrong empty group.

```bash
syncerd git-sync --once --config mirror.yaml
```

Expect per repository:

```
mirror gh-to-gl: acme/syncerd-rbtest-one -> syncerd-rbtest-one (N pushed, 0 deleted)
```

and a closing line:

```
Git mirror run finished in Xs: 2 mirrored, 0 skipped, 0 failures
```

Verify:

```bash
cat /tmp/gitsync-rb/cache/git-state.json | head -20
ls /tmp/gitsync-rb/cache/gh-to-gl/          # one bare cache dir per repo
git ls-remote https://gitlab.com/YOUR_GITLAB_GROUP/syncerd-rbtest-one.git
```

The `ls-remote` output must show every branch and tag from the source. Only
`refs/heads/*` and `refs/tags/*` are mirrored; host internal namespaces such as
`refs/pull/*` and `refs/merge_requests/*` are deliberately excluded.

---

## Phase 5: Idempotency

Run the exact same command again with nothing changed:

```bash
syncerd git-sync --once --config mirror.yaml
```

```
mirror gh-to-gl: acme/syncerd-rbtest-one unchanged, skipping
Git mirror run finished in Xs: 0 mirrored, 2 skipped, 0 failures
```

This is the fingerprint check. It runs `git ls-remote` against the source,
hashes the ref set, and compares it to state. An unchanged repository is never
cloned and never pushed, so a steady-state run is nearly free. No Slack message
is sent when nothing happened.

---

## Phase 6: Change detection

Push a new branch to one source repo, then rerun:

```bash
# in a clone of syncerd-rbtest-one
git checkout -b feature/runbook
git commit --allow-empty -m "runbook test"
git push origin feature/runbook
```

```bash
syncerd git-sync --once --config mirror.yaml
```

Expect exactly one repository to move and the other to skip:

```
Git mirror run finished in Xs: 1 mirrored, 1 skipped, 0 failures
```

Confirm the branch arrived:

```bash
git ls-remote https://gitlab.com/YOUR_GITLAB_GROUP/syncerd-rbtest-one.git 'refs/heads/feature/*'
```

---

## Phase 7: Prune

Delete that branch at the source and rerun:

```bash
git push origin --delete feature/runbook
syncerd git-sync --once --config mirror.yaml
```

```
mirror gh-to-gl: acme/syncerd-rbtest-one -> syncerd-rbtest-one (0 pushed, 1 deleted)
```

Confirm it is gone at the destination:

```bash
git ls-remote https://gitlab.com/YOUR_GITLAB_GROUP/syncerd-rbtest-one.git 'refs/heads/feature/*'
# expect no output
```

To see the safer modes, set `push_mode: additive` on the mirror and repeat: the
destination branch survives a source deletion. `push_mode: fast-forward` refuses
any non fast forward update and reports it as a run failure.

---

## Phase 8: The adopt guard

This is the protection worth verifying yourself, because it is what stands
between a misconfigured mirror and someone's work.

Simulate a first run against a destination that already has content:

```bash
# Throw away the state so SyncerD forgets it has ever seen these destinations
rm /tmp/gitsync-rb/cache/git-state.json

syncerd git-sync --once --config mirror.yaml
```

Expect a failure per repository, not a push:

```
destination already has N refs and this mirror has no prior state; set adopt: true on mirror "gh-to-gl" to overwrite it
Git mirror run finished in Xs: 0 mirrored, 0 skipped, 2 failures
```

Confirm the destination is untouched. Then opt in deliberately:

```bash
# add "adopt: true" under the gh-to-gl mirror, then
syncerd git-sync --once --config mirror.yaml
```

Now it proceeds. Remove `adopt: true` again afterwards.

The same guard fires when you **retarget** an existing mirror. Change
`name_template` on a mirror that already has state and you will see:

```
mirror gh-to-gl: acme/syncerd-rbtest-one destination changed from "syncerd-rbtest-one" to "...", treating as a first run
```

followed by the adopt refusal if that new destination already holds refs.

---

## Phase 9: Reverse direction

Any provider can be either end. Add a second mirror going back:

```yaml
    - name: gl-to-gh
      source: gl
      destination: gh
      filters:
        include: ["syncerd-rbtest-*"]
      create_missing: true
```

Both mirrors run in the same invocation and keep separate state, keyed by mirror
name. Watch for two `N repositories discovered` lines.

---

## Phase 10: Slack

Add the same webhook block image sync already uses:

```yaml
slack:
  enabled: true
  webhook_url: "https://hooks.slack.com/services/..."
  notify_on_new: true
  notify_on_error: true
  message_format: detailed    # groups by mirror name; "compact" is the default
```

Slack uses an **incoming webhook URL**, not a bot token. There are no OAuth
scopes to configure.

Then force a change and rerun. Expect one Block Kit message summarising the run.
Confirm:

- a dry run posts **nothing**,
- a run where everything was skipped posts **nothing**,
- no token appears anywhere in the message.

To see a failure message, point a mirror at a group your token cannot write to.

---

## Phase 11: Scheduled mode

Drop `--once` to run the cron loop:

```bash
# add: schedule: "*/5 * * * *"  under git:
syncerd git-sync --config mirror.yaml
```

It runs once immediately, then on the schedule, until SIGINT or SIGTERM. An
invalid cron expression is rejected before the first run. If a run outlives its
interval the next tick is skipped rather than started concurrently.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `git is required for git-sync but was not runnable` | no `git` on PATH | install git 2.30+ |
| `git X.Y found, but git-sync requires 2.30 or newer` | old git | upgrade |
| `another git-sync is already running against work_dir "..."` | a second process holds the cache lock | wait, or check for a stuck process; the lock releases automatically if the process died |
| `destination already has N refs and this mirror has no prior state` | adopt guard | intended; confirm the destination is right, then set `adopt: true` |
| `refusing a mirror push from an empty cache` | source resolved to zero refs | check the source repo and token; this guard stops a full destination wipe |
| `cannot inspect destination and create_missing is off` | destination unreachable | fix credentials or network; the tool fails closed rather than pushing blind |
| `source repositories "a" and "b" both render to destination "x"` | two sources collide under `name_template` | make the template produce distinct names |
| `provider "x" has unsupported type "..."` | the type string is not one of the five registered types | use `github`, `gitlab`, `bitbucket`, `azuredevops`, or `codecommit` |
| Everything skipped, nothing mirrored | fingerprints match | expected; delete the state file to force a full pass |
| Every repo fails the adopt guard on run two | state file was lost between runs | see `state_path` below |

Useful inspection commands:

```bash
# what the tool thinks it has already done
cat /tmp/gitsync-rb/cache/git-state.json | python3 -m json.tool

# the clone cache
ls -la /tmp/gitsync-rb/cache/gh-to-gl/

# what the source actually advertises
git ls-remote --heads --tags https://github.com/ORG/REPO.git
```

---

## Gotchas

**`state_path` and `work_dir` default to different places.** `work_dir` defaults
to `/var/lib/syncerd/git`, an absolute path suited to a mounted volume, while
`state_path` defaults to `.syncerd-git-state.json`, relative to the working
directory. If you mount a volume for the cache but not for state, state is lost
on every restart, and from the second run onward **every repository fails the
adopt guard** against destinations it populated itself. Set both explicitly,
under the same volume, as this runbook's config does. This default needs
settling before the Helm chart lands.

**State is written at the end of a run.** A run killed partway through loses the
fingerprints for repositories it already pushed, producing the same adopt guard
storm on the next run. Recover by setting `adopt: true` for one run, then
removing it. Incremental saving is a known follow-up.

**All five provider types run.** `github`, `gitlab`, `bitbucket`, `azuredevops`,
and `codecommit` all construct and mirror successfully; any of them can be a
source or a destination. Three real, permanent limitations remain, so a
config that looks valid can still fail at the credential or network step:

- **CodeCommit**: SyncerD does not derive SigV4 git credentials, so IRSA and
  instance roles cover listing and creating repositories, but the git
  transport needs static IAM HTTPS Git credentials (`git_username` /
  `git_password`, or `SYNCERD_GIT_<NAME>_GIT_USERNAME` /
  `SYNCERD_GIT_<NAME>_GIT_PASSWORD`), which are only issuable to an IAM user.
- **Azure DevOps Entra mode**: the operator supplies the access token;
  SyncerD does not acquire one from Azure AD. It is supplied through the
  same `SYNCERD_GIT_<NAME>_TOKEN` variable used for a PAT.
- **Bitbucket**: Cloud only. The `api_url` override changes the host but the
  request paths are Cloud shaped, so Bitbucket Data Center is not supported.
  Bitbucket also has no archived concept, so `skip_archived` has no effect
  for a Bitbucket source.

**A personal account source needs a token belonging to that account.**
GitHub's `GET /users/{owner}/repos` returns public repositories only, whatever
token is presented, so SyncerD lists a personal account through
`GET /user/repos` instead, which reports private repositories too. That
endpoint only ever describes the account the token belongs to. Mirroring
someone else's personal account therefore discovers its public repositories
and nothing more, because no credential can see further. Organisation owners
are unaffected: `GET /orgs/{org}/repos` reports private repositories to any
token with access. The same shape applies to GitLab, where a user namespace
falls back to `GET /users/{owner}/projects`.

**Released binaries need git on PATH.** goreleaser ships a bare binary with
no bundled git; install git 2.30 or newer on any host that runs a release
binary. The Docker image installs it for you.

**Branches, tags, and optionally pull request heads are mirrored.** Not issues,
pull request discussions, wikis, releases, or Git LFS objects. A repository
using LFS mirrors its pointers, not its objects. With `pull_requests.enabled`
set on a mirror, the commits behind each open pull request opened from a fork
are pushed as a branch; see Mirroring pull request heads above.

---

## Mirroring pull request heads

```yaml
git:
  mirrors:
    - name: gh-to-gl
      source: gh
      destination: gl
      pull_requests:
        enabled: true             # default false
        branch_prefix: syncerd/pr # default
        states: [open]            # default, and the only accepted value today
```

A pull request opened from a branch of the source repository needs nothing: its
commits already arrive with the ordinary branch mirror, under their own branch
name. A pull request opened from a **fork** has no branch in the source
repository, so its commits reach the destination only with this setting on, as
`refs/heads/<branch_prefix>/<number>`.

Under the default `mirror` push mode the branch disappears from the destination
on the first run after the pull request closes: the source stops listing it, and
prune removes what the source no longer has.

### Recreating the pull requests themselves

Set `mirror_objects` and each open source pull request is recreated at the
destination as a real pull request, updated on every run, with its discussion,
its inline review comments, and its review verdicts:

```yaml
      pull_requests:
        enabled: true
        mirror_objects: true      # default false, requires enabled
        comments: true            # default true once mirror_objects is on
        reviews: true             # default true once mirror_objects is on
        labels: true              # default true
```

With `mirror_objects` on, **every** mirrored pull request gets a
`branch_prefix` branch, including one whose head is already a branch of the
source repository. A destination pull request has to name one head branch, and
a uniform name is what keeps the mapping simple. With it off, the P1 rule
stands: fork heads only.

**A merged source pull request is closed at the destination, not merged.** A
merge performed at the destination would create a merge commit that differs
from the source's, and the branch mirror then force overwrites it, so the
destination would report a merge whose commit no longer exists. SyncerD closes
the pull request instead and comments naming the source merge commit, which by
then genuinely is on the destination's base branch.

**Review verdicts are mirrored as text, never as real approvals.** An approval
posted by SyncerD's token would be a review nobody performed, and on a
destination with required approvals it would satisfy a rule no human satisfied.

**Mentions and issue references are neutralised.** `@someone` in a mirrored
body would otherwise notify whichever destination account owns that handle, and
`#123` would cross link to an unrelated destination issue. Both are rewritten
to render identically and link to nothing. Text inside fenced code blocks is
left byte for byte.

Everything SyncerD writes carries a hidden marker. That is how a later run
updates its own comments in place instead of posting duplicates, and how a
destination pull request is re-identified after a lost state file, so losing
state causes re-inspection rather than a second copy of everything.

Only comments SyncerD wrote are ever edited or deleted. A comment written by a
person at the destination is left alone, even though the source is the
authority: the source is authority over what it published, not over what
somebody else said.

### What survives the crossing

Every provider type can mirror pull requests, in any direction. They do not
agree on what a pull request has, so some things are approximated and some
are lost. This table is the whole of it:

| | GitHub | GitLab | Bitbucket | Azure DevOps | CodeCommit |
|---|---|---|---|---|---|
| Reopen a closed mirror | yes | yes | **no** | yes | **no** |
| Draft | yes | via a `Draft:` title prefix | yes | on create only | **no** |
| Update watermark | yes | yes | yes | **none reported, so every run reconciles** | yes |
| Labels | yes | yes | **no** | yes | **no** |
| Inline comment anchors | yes | needs the source to report commit SHAs | yes | yes | yes |
| Comment deletion | removed | removed | removed | **tombstone** | **content blanked** |

Nothing here fails a run. A capability the destination lacks is logged and
skipped, never faked:

- **Reopen.** Bitbucket has no reopen endpoint and CodeCommit permits only
  `OPEN` to `CLOSED`. A destination pull request closed by hand while its
  source is still open stays closed, and SyncerD warns once and records the
  divergence rather than opening a second pull request for the same work.
  Nothing else is attempted on it, because both providers reject an update
  to a closed pull request. Reopen it by hand and the next run picks it up
  again: the recorded divergence is re-checked, never trusted.
- **Draft.** GitLab has no draft field, so a draft is expressed by prefixing
  the title with `Draft:`, and the prefix is stripped when the source leaves
  draft. This is the one place SyncerD does not copy a title verbatim.
  CodeCommit ignores the flag.
- **Labels.** Ignored on Bitbucket and CodeCommit, which have none.
- **Inline comments.** GitLab and CodeCommit refuse to anchor a comment
  without commit SHAs. When the source did not report them, the comment is
  posted as a discussion comment naming the file and line instead of being
  dropped.
- **Deletion.** Azure DevOps and CodeCommit delete softly, so a comment
  deleted at the source leaves a tombstone or an empty comment rather than
  disappearing.

CodeCommit is also the expensive one: its API returns pull request ids only,
so each listing costs one extra call per pull request, and it cannot search
by branch at all, so SyncerD enumerates once per repository per run and
matches locally. A lookup that misses among the open pull requests
enumerates the closed ones too, which is what stops a lost state file from
producing a duplicate.

Azure DevOps reports no update timestamp on a pull request, so there is
nothing to watermark against and every pull request is reconciled on every
run. That is slower than the other four and it is the only way to notice an
edit there at all.

**Read this before enabling it.** A fork pull request head is code written by
anyone who can open a pull request against the source. Turning this on pushes
that code to a destination branch, where it arrives as an ordinary push from a
trusted mirror rather than as a fork pull request. If destination CI builds on
branch push, it will run that code with whatever credentials that CI holds, and
none of the source's fork pull request protections apply. Restrict destination
CI to the branches your team actually builds and exclude the `branch_prefix`
namespace. It is one contiguous namespace so a single glob covers it.

A repository whose own branches live under `branch_prefix` is refused with an
error naming the branch, because mirroring both would have the two overwrite
each other on every run. Change `branch_prefix` for that mirror, or exclude the
repository.

Only GitHub sources can list pull requests today. A mirror that enables
`pull_requests` with any other source type is rejected at startup.

---

## Live provider tests

Every other test in this repository checks SyncerD against fakes written by
whoever wrote the code, and that has already proved insufficient: three
defects that made pull request mirroring inoperable survived a green suite,
a passing verification run, and a review of their own change, because the
fakes agreed with the mistake. A real API does not.

```bash
export SYNCERD_LIVE_GITHUB_TOKEN=ghp_...   # repo scope
export SYNCERD_LIVE_GITHUB_OWNER=your-account
make test-live
```

It creates two throwaway private repositories named `syncerd-live-*`, seeds
one with a branch, opens a pull request, mirrors the branches and then the
pull request to the other, and checks what actually landed: that the
destination pull request exists and is findable by its head branch, that
the body carries the marker and no live mention, that a second run changes
nothing, that a comment mirrors exactly once, and that closing the source
closes the mirror. Then it deletes both repositories. Set
`SYNCERD_LIVE_KEEP=1` to leave them behind and look.

Run it before a release that touches provider code. It is the only check
that would have caught the defects above.

---

## Cleanup

```bash
rm -rf /tmp/gitsync-rb
# then delete the throwaway repos in GitHub and GitLab
```
