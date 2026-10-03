# OCR review and OpenCode repair on Alpaca

Both legs default to the existing local `bullmoose-ocr:20b` Ollama alias (GPT-OSS
20B, 65,536 context) at `http://127.0.0.1:11434/v1`. No subscription, credits,
purchased pack, or paid fallback is required. OCR is advisory, not a required
check or approval. Every repair requires a new, explicit writer `/oc` request.

## Activation requires owner approval

The integration ships disabled. On 2026-10-03 the separate repository runner
`alpaca-call-me-maybe-ai` was registered at
`~/.local/share/call-me-maybe-ai/runner`. Its LaunchAgent is installed, stopped,
and has `RunAtLoad=false`. Bullmoose's runner, configuration and model weights
were not changed. Do not register it again.

After approving and merging this PR, on Alpaca:

```bash
plutil -replace RunAtLoad -bool true ~/Library/LaunchAgents/actions.runner.ericdmoore-call-me-maybe.alpaca-call-me-maybe-ai.plist
(cd ~/.local/share/call-me-maybe-ai/runner && ./svc.sh start)
gh variable set AI_REVIEW_ENABLED --body true -R ericdmoore/call-me-maybe
# Enable only when repository-writing repair automation is approved:
gh variable set AI_REPAIR_ENABLED --body true -R ericdmoore/call-me-maybe
```

Set either variable to `false` to stop accepting new work. Stop the runner with
`./svc.sh stop` from its directory; cancel in-flight GitHub runs separately.
Use launchd/LaunchAgents, never systemctl, on this Mac.

## Models and credentials

| Leg | Repository variable | Default |
| --- | --- | --- |
| Review | `OCR_MODEL` | `ollama/bullmoose-ocr:20b` |
| Repair | `OPENCODE_MODEL` | `ollama/bullmoose-ocr:20b` |

An explicit OpenRouter choice uses `openrouter/vendor/exact-model-id`, for example
`openrouter/openai/gpt-oss-20b`. Verify availability and price with the provider
before selecting it; this is not a free-model promise. Unsupported providers,
malformed identifiers and missing credentials fail rather than fall back.
For a manual second OCR review, use Actions → OCR review → Run workflow on
`main`, enter the PR number and set `model` to an exact OpenRouter identifier.
Leave it blank for the repository default. The equivalent CLI command is:

```bash
gh workflow run ocr-review.yml --ref main -R ericdmoore/call-me-maybe \
  -f pr=NUMBER -f model=openrouter/openai/gpt-oss-20b
```

This override applies only to that review; automatic reviews and repair re-reviews
keep `OCR_MODEL` (local by default). Run it after the first review completes:
reviews for the same PR share a concurrency group. The second pass updates the
sticky summary with its model and coverage, preserves previous inline findings,
and adds non-overlapping findings. Each run retains its own JSON artifact.
These controls become available after the integration is merged and activated.

A writer can independently override the repair model for one request:

```text
/oc --model openrouter/openai/gpt-oss-20b validate the findings
```

No OpenRouter credential or GitHub App credential is configured in this repository.
Nothing was copied from Bullmoose. You do not need to be at Alpaca: add
`OPENROUTER_API_KEY` through this repository's GitHub Settings → Secrets and
variables → Actions, using an OpenRouter key obtained from your account. Do not
paste the key into a PR comment or workflow input. Alternatively, use gh's hidden
prompt from any authenticated computer:

```bash
gh secret set OPENROUTER_API_KEY --repo ericdmoore/call-me-maybe
```

Set `OCR_MODEL` or `OPENCODE_MODEL` separately with `gh variable set ... --body ...`.
Returning both to their defaults restores the wholly local route. Even repair's
small/summary model is explicitly the selected model; provider selection is restricted.

## Workflow and trust boundaries

- Ready same-repository PRs are reviewed on open, update, reopen, readiness and
  description edits. Manual dispatch: `gh workflow run ocr-review.yml --ref main
  -f pr=NUMBER`. Reviews include tests, plans, designs and repository instructions.
  Linked closing issues and base-revision Markdown design links supply requirement
  evidence. The 8,000-character context ceiling is handled with labelled excerpts.
- Review authorization runs on GitHub-hosted Linux before scheduling Alpaca.
  Alpaca checks out trusted policy and reads PR Git objects; it never executes PR
  build scripts for review. Fork and draft PRs are rejected, including manual dispatch.
- `/oc` at the start of a PR conversation or an inline reply requests one repair
  pass. The hosted inline relay dispatches the default-branch worker; it does not
  run a privileged self-hosted job from a PR revision. The worker fetches the
  comment from GitHub and verifies the commenter currently has write, maintain or
  admin access, the PR is ready and same-repository, and an inline request targets
  the current head. Comments and code are evidence, not authorization to expand scope.
- A bot-owned request marker consumes each comment once, including failure or
  cancellation. Workflow reruns do not run another pass. Post a new `/oc` to retry.
  Edited comments do not trigger repairs. Model reviews never request repairs.
- Upstream OpenCode reads the OCR summary, inline findings and replies, linked
  requirements and repository instructions. It validates findings, repairs confirmed
  defects with regression tests, replies to invalid findings with concrete evidence
  using `gh`, and explains unresolved intent. It leaves changes uncommitted.
- Trusted workflow code owns commit and push. It refuses changed heads, unexpected
  branch updates, agent-created commits, non-fast-forward history, and edits to
  automation, hooks, model settings or coverage floors. It never force-pushes or
  merges. A trusted pre-push hook runs the repository validation and checks the
  remote head again afterwards. No changes is a valid result for invalid findings.

This is a personal-machine trusted-writer workflow, **not an OS sandbox**. Repair
executes trusted PR code and tests with the runner account's Unix permissions.
Never extend it to forks. Separate runner registration, checkout, job directories,
child home/XDG/gh state and tokens prevent accidental cross-repository reuse; they
do not protect the machine against a malicious repository writer. OpenCode uses
`--pure`, disables project configuration, and receives job-local model settings.
No login-shell HOME or persistent OCR/OpenCode configuration is modified.

The dedicated runner also has a **host-installed job-start guard**, outside all
checkouts, configured through its own `.env`. It only accepts the two approved
workflow paths at `refs/heads/main` and the expected event types. It rejects fork
PR events and PR-defined workflows even if they request this runner's public label.
On rejection it terminates that job's Runner.Worker before steps execute, since
an exit code alone could be bypassed with `if: always()`. The listener stays alive.
Its source is `.github/ai/runner-guard.py` and `runner-started.sh`; updating the
host copy is a deliberate operator action, not a job step. The guard's predicate
is covered by tests, and a disposable Worker process verified hard-stop behavior.
Actual GitHub runner-hook context still needs the approved activation trial.

## Shared tooling and model contention

OCR is the existing Homebrew `/opt/homebrew/bin/ocr` at **1.12.11**. The workflow
checks the version; it does not reinstall or upgrade it. Publishing uses the
upstream helper at commit `a758d9cbfb689937c7857ad64b2dd66adb58c0c2`.
The small CLI adapter avoids the upstream composite action's fixed `/tmp` output
paths, npm reinstallation and persistent config writes on a shared Mac. Inline
publication, sticky summary and history handling remain upstream code. Progress
streams live; JSON and stderr artifacts are retained seven days. Coverage counts,
failed/waived files and context limitations remain visible even with zero findings.
Telemetry stays off. Job-private config, credentials and session traces are removed.

OpenCode **1.18.34** is installed once at
`~/.local/share/ai-tools/opencode/1.18.34/node_modules/.bin/opencode`, reusable by
other repositories without sharing sessions or credentials. Its version is checked
before every pass. The runner archive was reused from the existing installation;
its SHA-256 matched the upstream 2.337.0 macOS ARM64 release digest. GitHub manages
subsequent runner updates. Go, Node, gh, mandoc, shellcheck and actionlint are shared.

GitHub concurrency is repository-local. It cannot serialize against Bullmoose.
Alpaca's current Ollama service has `OLLAMA_NUM_PARALLEL=1` and
`OLLAMA_MAX_LOADED_MODELS=1`; the existing server queue serializes model requests
from both repositories. Reviews use one subtask, 32K prompt ceiling, 20-minute
request timeout and bounded overall budgets. Jobs can interleave between requests;
this is not exclusive whole-job scheduling. Heavy competing work can time out or
exhaust a budget and must report incomplete coverage. There is no automatic cloud
fallback. If server parallelism is later increased, reassess memory and both runners
before enabling it; do not assume the GitHub concurrency keys protect the Mac.

## Validation and token behavior

Before pushing, the hook runs formatting, vet, both custom analyzers and their
tests, race tests and per-package coverage floors/ratchet, every config example
(with placeholder acceptance and strict rejection), template lint, init/check
round-trip, JSON schema checks, regenerated site assets, man-page lint, npm site
build, Linux amd64/arm64/armv7 cross-compilation, shellcheck and integration tests.
Configuration shape comes from `doorman schema`; validity comes from `doorman check`.
No test requires Asterisk or real caller IDs/PINs. Floors must never be lowered.

Repair uses the repository-scoped `GITHUB_TOKEN`: contents write for pushing,
pull-requests write for replies, actions write for explicit follow-up dispatch.
It is not a PAT or a borrowed Bullmoose credential. **A GITHUB_TOKEN push does not
establish that normal CI ran.** After a recorded push, the workflow explicitly
queues `ci.yml` with `expected_head=NEW_SHA` and OCR with the same head and a cheap
150K-token, low-effort review. Every CI job checks out that exact SHA. Dispatch
may require human approval or fail; the repair run is never proof that CI passed.
Inspect Actions and verify that all CI jobs for the reported SHA succeeded before
merging. Workflow-dispatch CI may not satisfy a branch-protection PR check; use the
normal approval path, or separately configure an approved repository-scoped GitHub
App for event-generating pushes. No App integration is claimed here.

If dispatch fails after a successful push, explicitly queue:

```bash
gh workflow run ci.yml --ref PR_BRANCH -f expected_head=NEW_SHA -R ericdmoore/call-me-maybe
gh workflow run ocr-review.yml --ref main -f pr=NUMBER -f head=NEW_SHA -f cheap=true -R ericdmoore/call-me-maybe
```

## Trial and verification record

Reference inspected on 2026-10-03: Bullmoose [#425](https://github.com/ericdmoore/bullmoose.cc/pull/425)
merged on October 2 with green checks. [#428](https://github.com/ericdmoore/bullmoose.cc/pull/428)
was open at `45e913269890a288f691362ad2623b804150f708`, with all reported checks green.
The corrected authorization module and inline review discussion informed this
adaptation. Missing-variable guards and exact model-ID validation are included;
write permissions are limited to the publishing/repair jobs that need them.

Local verification includes authorization/model/stale-head/validation-failure
regressions, YAML/shell lint and repository checks. A disposable local Go fixture
removed a zero-denominator guard. OCR reported the requirement violation with one
selected/completed file and no failures. OpenCode 1.18.34 independently read the
file and confirmed the panic through the same local model. Neither trial published,
pushed or used a paid provider. An attached-prompt repair dry-run stopped
without edits or a final assessment; the workflow now rejects that outcome. A fresh
pass using a direct prompt repaired the guard, added zero/nonzero regression cases
and passed Go tests. The new test was independently confirmed to fail against the
original defect. A cheap OCR re-review selected both the repaired code and its
regression test, completed both, and reported no findings. The integration uses
that direct prompt form. GitHub push and publication behavior still require the
trial below. Baseline generated site assets were stale; this PR
regenerates them from the current sources so the existing CI freshness check passes.

**Still unverified until activation:** actual GitHub inline/sticky publishing,
inline relay, unattended repair commit/push and follow-up CI/OCR dispatch. After
owner approval and merge, use a small disposable same-repository branch:

1. Add a tiny pure Go function plus test with a deliberately missing zero guard;
   state the expected behavior in the PR body and make the PR ready.
2. Confirm automatic OCR logs, JSON artifact, inline finding, coverage and sticky
   summary. Manual-dispatch once to confirm the same summary is updated.
3. Post `/oc validate the findings and add a regression test` as a repository writer.
   Check its evidence/replies, tests and ordinary push to the existing branch.
4. Confirm CI and cheap OCR run against the new SHA; approve pending runs explicitly.
   Confirm no further repair starts. Exercise an inline `/oc` reply separately.
5. Exercise a changed-head request and a failing check: neither may push. Unit tests
   already cover fork/read-only rejection; do not execute fork code as a live test.
   Close the trial PR without merging the intentional fixture.
