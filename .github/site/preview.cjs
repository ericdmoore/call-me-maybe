const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const {execFileSync} = require('node:child_process');

const marker = '<!-- cmm-site-preview -->';
const worker = 'callmemaybe';
const account = 'cf473a1c1e6f51585477ccf5216ae636';
function wranglerPath(env = process.env, home = os.homedir()) {
  // A host-managed link, never PATH resolution through a repository's node_modules.
  const binary = env.WRANGLER_BIN || path.join(home, '.local/bin/wrangler');
  if (!path.isAbsolute(binary)) throw new Error('WRANGLER_BIN must be an absolute host-managed path');
  return binary;
}

function eligible(pr, repo, head, closing) {
  // A preview is only ever created for a PR into main, but a PR retargeted
  // before it closed still has one to remove.
  return (closing || pr.base.ref === 'main') && pr.head.repo?.full_name === repo &&
    pr.head.sha === head && pr.state === (closing ? 'closed' : 'open');
}
async function current({github, context}) {
  const original = context.payload.pull_request;
  const {data: pr} = await github.rest.pulls.get({...context.repo, pull_number: original.number});
  return eligible(pr, `${context.repo.owner}/${context.repo.repo}`, original.head.sha,
    context.payload.action === 'closed');
}
// Files Wrangler reads from the asset root as deployment configuration. The
// site has none; one arriving in a PR's artifact is the PR configuring the
// preview host's redirects or response headers, which the generated config is
// meant to rule out.
const assetConfig = new Set(['_headers', '_redirects', '_routes.json', '.assetsignore']);
function validateAssets(directory, root = directory) {
  // Treat the artifact only as static data, never as config or executable code.
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) validateAssets(file, root);
    else if (!entry.isFile()) throw new Error(`Non-regular asset: ${entry.name}`);
    else if (directory === root && assetConfig.has(entry.name)) throw new Error(`Deployment config in assets: ${entry.name}`);
  }
}
function config(directory) {
  return {name: worker, account_id: account, compatibility_date: '2026-07-29',
    workers_dev: true, previews: {},
    assets: {directory, not_found_handling: '404-page'}};
}
function previewURL(output) {
  const urls = output?.preview?.urls;
  if (!Array.isArray(urls) || typeof urls[0] !== 'string' || !urls[0].trim())
    throw new Error('Cloudflare returned no preview URL');
  let url;
  try { url = new URL(urls[0]); }
  catch { throw new Error('Cloudflare returned an invalid preview URL'); }
  if (url.protocol !== 'https:' || !url.hostname.endsWith('.workers.dev') || url.username || url.password)
    throw new Error('Unexpected Cloudflare preview URL');
  return url.href;
}
async function deploy({github, context, core}) {
  if (!await current({github, context})) {
    core.setOutput('stale', 'true');
    return;
  }
  const directory = process.env.PREVIEW_DIR;
  const closing = context.payload.action === 'closed';
  if (!closing) {
    validateAssets(path.join(directory, 'dist'));
    if (!fs.statSync(path.join(directory, 'dist/index.html')).isFile()) throw new Error('Missing index.html');
  }
  fs.writeFileSync(path.join(directory, 'wrangler.json'), JSON.stringify(config('./dist')));
  // Explicit config and a fresh cwd prevent PR-controlled Wrangler, .env or build hooks.
  const args = ['preview', ...(closing ? ['delete', '--skip-confirmation'] : []),
    '--config', path.join(directory, 'wrangler.json'), '--name', `pr-${context.payload.number}`];
  if (!closing) args.push('--json', '--ignore-base-config');
  const outputFile = path.join(directory, 'wrangler-output.jsonl');
  const env = childEnv(process.env, {CI: 'true', WRANGLER_SEND_METRICS: 'false', WRANGLER_OUTPUT_FILE_PATH: outputFile});
  try {
    execFileSync(wranglerPath(), args, {cwd: directory, encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 180000, maxBuffer: 8 * 1024 * 1024, env});
  } catch (error) {
    // A failed build may never have created a preview; close/rerun is idempotent.
    if (!closing || !String(error.stderr).includes('Preview not found. [code: 10025]')) throw error;
  }
  if (!closing) {
    const records = fs.readFileSync(outputFile, 'utf8').trim().split('\n').map(line => JSON.parse(line));
    const result = records.findLast(row => row.type === 'preview');
    const url = previewURL({preview: {urls: result?.preview_urls}});
    // Cloudflare propagation can take a few seconds.
    execFileSync('/usr/bin/curl', ['--fail', '--silent', '--show-error', '--retry', '6',
      '--retry-all-errors', '--retry-delay', '5', '--max-time', '20', '--output', '/dev/null', url],
    {timeout: 180000, env});
    core.setOutput('url', url);
  }
}
// The github-script step's environment carries the job's GITHUB_TOKEN as
// INPUT_GITHUB-TOKEN. Wrangler and curl need the OAuth login under HOME and
// their own settings, never a GitHub credential.
function childEnv(source, extra) {
  const env = {};
  for (const [key, value] of Object.entries(source)) {
    if (['HOME', 'PATH', 'TMPDIR', 'LANG', 'LC_ALL', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME'].includes(key) ||
      /^(WRANGLER|CLOUDFLARE)_/.test(key)) env[key] = value;
  }
  return {...env, ...extra};
}
async function comment({github, context}, result, url) {
  if (!await current({github, context})) return;
  const closing = context.payload.action === 'closed';
  const sha = context.payload.pull_request.head.sha;
  const run = `${context.serverUrl}/${context.repo.owner}/${context.repo.repo}/actions/runs/${context.runId}`;
  // A failed removal must not read as "nothing deployed": the preview is live.
  let status = closing ?
    `Preview could not be removed after this PR closed; it is still published. See the [workflow run](${run}).` :
    `Preview failed for \`${sha}\`. See the [workflow run](${run}).`;
  if (result === 'success') {
    if (closing) status = 'Preview removed because this PR is closed.';
    else {
      // A missing/malformed output must not suppress the failure notification.
      try {
        const link = previewURL({preview: {urls: [url]}});
        status = `**[Open site preview](${link})**\n\nCommit: \`${sha}\`\n\nThis URL updates with this PR. [Workflow run](${run}).`;
      } catch {
        status += ' The deployment did not provide a valid preview URL.';
      }
    }
  }
  const body = `${marker}\n${status}`;
  const params = {...context.repo, issue_number: context.payload.number};
  const comments = await github.paginate(github.rest.issues.listComments, {...params, per_page: 100});
  const existing = comments.find(c => c.user?.login === 'github-actions[bot]' && c.body?.startsWith(marker));
  if (existing) await github.rest.issues.updateComment({...context.repo, comment_id: existing.id, body});
  else if (!closing) await github.rest.issues.createComment({...params, body});
}
module.exports = {wranglerPath, eligible, current, config, validateAssets, previewURL, childEnv, deploy, comment};
