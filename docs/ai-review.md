# OCR review and OpenCode repair on Alpaca

Both legs default to `ollama/gpt-oss:20b` (local GPT-OSS 20B, 65,536 context)
at `http://127.0.0.1:11434/v1`. No subscription, credits,
purchased pack, or paid fallback is required. OCR is advisory, not a required
check or approval. Every repair requires a new, explicit writer `/oc` request.

## Activation requires owner approval

The integration ships disabled. After owner approval on 2026-10-03, both review
and writer-requested repair were enabled. The separate repository runner
`alpaca-call-me-maybe-ai` was registered at
`~/.local/share/call-me-maybe-ai/runner`. Its LaunchAgent is running
with `RunAtLoad=true`. Bullmoose's runner, configuration and model weights
were not changed. Do not register it again.

For a future approved reactivation, on Alpaca:

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
| Review | `OCR_MODEL` | `ollama/gpt-oss:20b` |
| Repair | `OPENCODE_MODEL` | `ollama/gpt-oss:20b` |

The public local model name maps to Alpaca's existing `bullmoose-ocr:20b` serving
preset, which uses the same GPT-OSS 20B weights with `num_ctx=65536`. The stock
`gpt-oss:20b` installation has no context override. OCR uses that preset's API ID;
OpenCode uses its upstream model `id` mapping. No weights are downloaded and neither
model's persistent configuration is changed. The original repository variable
`ollama/bullmoose-ocr:20b` remains accepted and is normalized to `ollama/gpt-oss:20b`
in job output, so existing automatic reviews keep working through the transition.

An explicit OpenRouter choice uses `openrouter/vendor/exact-model-id`, for example
`openrouter/anthropic/claude-sonnet-5.5`. Verify availability and price with the provider
before selecting it; this is not a free-model promise. Unsupported providers,
malformed identifiers and missing credentials fail rather than fall back.
For a manual second OCR review, use Actions → OCR review → Run workflow on
`main`, enter the PR number and select `model` from the alphabetically sorted
dropdown. `default` keeps the repository setting; the explicit local choice and
curated OpenRouter choices apply to this run only. The OpenRouter IDs were checked
against its model catalog for tool support on 2026-10-03. The list is static: update
the workflow to add choices as the catalog changes.

| Model | Exact dropdown value |
| --- | --- |
| Local · GPT-OSS 20B | `ollama/gpt-oss:20b` |
| Claude Opus 5.5 | `openrouter/anthropic/claude-opus-5.5` |
| Claude Sonnet 5.5 | `openrouter/anthropic/claude-sonnet-5.5` |
| DeepSeek V4.1 Flash | `openrouter/deepseek/deepseek-v4.1-flash` |
| Gemini 3.8 Flash | `openrouter/google/gemini-3.8-flash` |
| Kimi K3 | `openrouter/moonshotai/kimi-k3` |
| GPT-6.1 Sol | `openrouter/openai/gpt-6.1-sol` |
| Qwen3 Coder Next | `openrouter/qwen/qwen3-coder-next` |
| Free · Qwen3.8 27B | `openrouter/qwen/qwen3.8-27b:free` |
| GLM 5.3 | `openrouter/z-ai/glm-5.3` |

`default` remains the first option, followed by these exact values sorted
alphabetically. GitHub's native choice input displays the value itself; friendly
names are documented here. The free Qwen option still requires the repository key
and is subject to OpenRouter's free-tier availability/rate limits; it never drops
the `:free` suffix or falls back to a paid model. It keeps the hosted review token
budgets, but does not require a dollar allowance for repairs.

The equivalent CLI command is:

```bash
gh workflow run ocr-review.yml --ref main -R ericdmoore/call-me-maybe \
  -f pr=NUMBER -f model=openrouter/anthropic/claude-sonnet-5.5
```

This override applies only to that review; automatic reviews and repair re-reviews
keep `OCR_MODEL` (local by default). Run it after the first review completes:
reviews for the same PR share a concurrency group. The second pass updates the
sticky summary with its model and coverage, preserves previous inline findings,
and adds non-overlapping findings. Each run retains its own JSON artifact.
These controls are available on the default branch.

A writer can independently override the repair model for one request:

```text
/oc --model openrouter/anthropic/claude-sonnet-5.5 validate the findings
```

The owner added this repository's `OPENROUTER_API_KEY` on 2026-10-03; only its
presence was verified. No GitHub App credential is configured, and nothing was
copied from Bullmoose. To replace the key, you do not need to be at Alpaca: add
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

## Runtime and spending budgets

Local reviews use OCR's native `--max-tokens-budget 0`: no aggregate token budget,
including after a repair. OCR 1.12.11 has no unlimited turn sentinel (`--max-tools 0`
means its default 100), so local runs use `--max-tools 2147483647`, effectively
unreachable before the clock expires. No per-group timeout is imposed locally.
OpenCode already has no configured step or aggregate token cap for local repairs.
Both stop normally when finished; this does not force them to loop until timeout.

| Setting | Repository variable | Default |
| --- | --- | --- |
| OCR inference deadline, all providers | `OCR_REVIEW_MINUTES` | 80 minutes |
| Cheap re-review inference deadline, all providers | `OCR_REREVIEW_MINUTES` | 20 minutes |
| Hosted OCR input + output allowance (paid/free) | `OCR_PAID_REVIEW_TOKENS` | 500,000 tokens |
| Hosted cheap re-review allowance (paid/free) | `OCR_PAID_REREVIEW_TOKENS` | 150,000 tokens |

Clock values must be whole minutes from 1 to 330. The job gets ten additional
minutes to publish failures/coverage, upload artifacts and clean up. Repair retains
its 120-minute job deadline, including validation. Model requests retain their
20-minute timeout. For example, to allow a four-hour review after this change merges:

```bash
gh variable set OCR_REVIEW_MINUTES --body 240 -R ericdmoore/call-me-maybe
gh variable set OCR_PAID_REVIEW_TOKENS --body 500000 -R ericdmoore/call-me-maybe
```

Provider routing determines the budget, including manual overrides: an OpenRouter
GPT-OSS model is a paid route, even though the same weights run locally for free.
Paid token budgets must be positive integers (maximum 2,147,483,647); `0`, negative
and malformed values fail before scheduling Alpaca. OCR checks reported aggregate
usage and forecasts new groups; it can stop early or overshoot by in-flight/final
requests. A token allowance is not an exact dollar ceiling.

OpenCode 1.18.34 has no native total-spend switch. Paid repairs therefore require
an OpenRouter key with a positive provider-enforced spending limit, remaining
allowance, and **Include BYOK in limit** enabled. Set the desired dollar amount and
reset period for this repository's existing key at
[OpenRouter Keys](https://openrouter.ai/settings/keys). The workflow checks the key's
[budget metadata](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key)
before starting OpenCode; absent/unlimited/exhausted budgets and lookup failures
stop paid repairs. Local repairs never contact that endpoint. No key values or raw
metadata are logged. No management key, proxy or new agent harness is needed.

This dollar allowance covers **all uses of that key for its configured period**,
not an independently reserved amount per job. Other calls consume it too, and a
period reset can replenish it during a run. Use a dedicated repository key and a
non-resetting cap if that is the intended total allowance. Configure provider-side
limits for paid reviews too when a dollar ceiling is wanted. The current secret's
spending settings have not been inspected or changed; paid execution remains untested.

Necessary context/output limits remain: Ollama's 65,536-token context, OCR's
32,768-token prompt ceiling and 16,384-token response limit, with upstream context
compression. OCR still stops on unusable responses/compression failures and keeps
its normal two review passes per group (one for cheap re-reviews), filtering and
file selection. These are review mechanics and model-capacity boundaries, not a
spending allowance. Removing budgets does not promise complete coverage; always
read the manifest and sticky summary.

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
The hook passed during the first automatic GitHub OCR review on 2026-10-03.

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
request timeout and clock deadlines. Only paid reviews have total token budgets.
Jobs can interleave between requests;
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
low-effort review (local: clock only; paid: 150K tokens by default). Every CI job
checks out that exact SHA. Dispatch
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

The first automatic [PR #38 review](https://github.com/ericdmoore/call-me-maybe/actions/runs/37171069516)
verified the runner hook, local inference, live logs, JSON artifact and sticky
publication. It completed 11 of 23 selected files; the previous 500K-token budget
blocked the remaining 12, correctly reported as incomplete despite a green
advisory workflow. The clock-only policy needs a post-merge trial.

**Still unverified live:** inline finding publication/relay, unattended repair
commit/push and follow-up CI/OCR dispatch. Use a small disposable same-repository branch:

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
