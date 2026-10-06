# Astro PR previews

`.github/workflows/site-preview.yml` publishes site changes on same-repository
PRs targeting `main`, including drafts. Opening, pushing or reopening a PR builds
its exact head on GitHub-hosted Ubuntu. Alpaca uploads only the static build to
Cloudflare Worker `callmemaybe`, using the named preview `pr-<number>`. One
`github-actions[bot]` comment is updated with its URL and commit; failures replace
the success message with a workflow link. Closing or merging deletes the preview
and updates the comment. Production deployment remains `npm run deploy` in `site`.

Removal lives in its own workflow, `.github/workflows/site-preview-cleanup.yml`,
which runs for every closed same-repository PR with no path or branch filter. A
trigger's filters also gate the close event, so a PR whose final diff no longer
touched the site, or that was retargeted off `main`, would otherwise leave its
public preview behind. Deleting a preview that never existed is a no-op. If the
removal itself fails, the comment says the preview is still published rather
than reporting a failed build.

Both workflows must be merged into `main` before `pull_request_target` can run them.
After merging, push a site change or reopen an existing site PR to test the whole
workflow. The `site/**`, `brand/**`, `.github/site/**` and preview workflow paths trigger the build.
Changes to Go-generated public files should include `make site-assets` outputs.
The ordinary CI site build still runs for all PRs, including forks.

## Alpaca setup

The repository runner `alpaca-call-me-maybe-ai` supplies compute and Wrangler's
existing OAuth login. No Cloudflare token is copied into GitHub. OAuth can expire
or be revoked; when upload fails, run `wrangler login` as Alpaca's runner user.
The computer must be awake and the runner online, or uploads remain queued.

Install the pinned upload CLI outside all checkouts:

```sh
npm install --prefix "$HOME/.local/share/wrangler/4.147.0" --save-exact wrangler@4.147.0
mkdir -p "$HOME/.local/bin"
ln -s "$HOME/.local/share/wrangler/4.147.0/node_modules/.bin/wrangler" "$HOME/.local/bin/wrangler"
```

The helper uses the standalone `~/.local/bin/wrangler` link. An optional
`WRANGLER_BIN` repository variable can name another absolute host-managed path;
it must not point into a PR checkout. Keep an existing link if already installed,
or deliberately repoint it when upgrading the pinned CLI.

The host job-start guard must include both preview workflows before activation.
After reviewing `.github/ai/runner-guard.py`, copy it to
`~/.local/share/call-me-maybe-ai/runner-guard.py`. The existing job-start hook loads
that file on each job; no runner restart is needed. Roll back that copy to revoke
preview access. Do not move the guard into a runner checkout. An installed guard
that predates `site-preview-cleanup.yml` refuses its jobs, which is the safe
direction: previews then stay up until the copy is refreshed.

## Trust and deployment boundary

`pull_request_target` runs workflow policy from the trusted base, not the PR.
The hosted build checks out the exact PR head with a read-only token and no
persisted Git credentials. `npm ci --ignore-scripts` disables dependency install
hooks; `npm --ignore-scripts run build` runs the requested build without pre/post
hooks. The build and Astro config still execute PR code on the disposable hosted
runner. These flags are not a sandbox. It uses no shared dependency cache. Fork PRs never
schedule Alpaca. The self-hosted job checks out only `github.workflow_sha`,
rechecks the current PR state and SHA, and downloads this run's artifact into a
fresh temporary directory. It never executes PR scripts or reads a PR Wrangler
config. The upload configuration contains only a fixed account, Worker name and
static assets: no routes, bindings, custom build commands or production secrets.
Symbolic links are refused, and so are `_headers`, `_redirects`, `_routes.json`
and `.assetsignore` at the asset root: Wrangler reads those from the asset
directory as deployment configuration, which would let a PR set the preview
host's redirects and response headers. The site ships none. Wrangler runs from
its host-installed location with an explicit configuration, a minimal
environment that carries no GitHub token, and ignores the dashboard Preview
base config.

A successful upload must pass an HTTP probe before the URL is posted. Runs for
one PR are serialized; stale runs are skipped, and a close waits for an upload
before deleting it. An old comment names the old commit while a new build is
pending. The comment job has write access on hosted Ubuntu; Alpaca has only a
read-only GitHub token. Preview content is public. OAuth grants the host broader
Cloudflare access than this workflow uses; this is not an OS sandbox against
trusted repository writers or other processes running as the same Unix user.

## Reuse for another static Astro repository

Copy the workflow and `.github/site/` helper/tests. Change the account, Worker,
runner labels, host CLI path and site directory; register the new repository's
trusted workflow in its host guard. Keep the hosted-build/static-upload split
and the exact-head checks. This pattern assumes `output: 'static'`; an SSR site
requires a separate review of executable Worker code and bindings.

Run `node --test .github/ai/*.test.cjs .github/site/*.test.cjs`, `actionlint`, and
`npm ci --ignore-scripts && npm --ignore-scripts run build` in `site` when changing this automation.

Cloudflare reference: https://developers.cloudflare.com/workers/previews/examples/
