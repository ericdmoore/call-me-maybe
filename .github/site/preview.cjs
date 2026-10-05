const fs = require('node:fs');
const path = require('node:path');
const {execFileSync} = require('node:child_process');

const marker = '<!-- cmm-site-preview -->';
const worker = 'callmemaybe';
const account = 'cf473a1c1e6f51585477ccf5216ae636';
const wrangler = '/Users/alpaca/.local/share/cmm-site-preview/node_modules/.bin/wrangler';

function eligible(pr, repo, head, closing) {
  return pr.base.ref === 'main' && pr.head.repo?.full_name === repo &&
    pr.head.sha === head && pr.state === (closing ? 'closed' : 'open');
}
async function current({github, context}) {
  const original = context.payload.pull_request;
  const {data: pr} = await github.rest.pulls.get({...context.repo, pull_number: original.number});
  return eligible(pr, `${context.repo.owner}/${context.repo.repo}`, original.head.sha,
    context.payload.action === 'closed');
}
function validateAssets(directory) {
  // Treat the artifact only as static data, never as config or executable code.
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) validateAssets(file);
    else if (!entry.isFile()) throw new Error(`Non-regular asset: ${entry.name}`);
  }
}
function config(directory) {
  return {name: worker, account_id: account, compatibility_date: '2026-07-29',
    workers_dev: true, previews: {},
    assets: {directory, not_found_handling: '404-page'}};
}
function previewURL(output) {
  const url = new URL(output.preview.urls[0]);
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
  try {
    execFileSync(wrangler, args, {cwd: directory, encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 180000, maxBuffer: 8 * 1024 * 1024,
    env: {...process.env, CI: 'true', WRANGLER_SEND_METRICS: 'false', WRANGLER_OUTPUT_FILE_PATH: outputFile}});
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
    {timeout: 180000});
    core.setOutput('url', url);
  }
}
async function comment({github, context}, result, url) {
  if (!await current({github, context})) return;
  const closing = context.payload.action === 'closed';
  const sha = context.payload.pull_request.head.sha;
  const run = `${context.serverUrl}/${context.repo.owner}/${context.repo.repo}/actions/runs/${context.runId}`;
  let status = `Preview failed for \`${sha}\`. See the [workflow run](${run}).`;
  if (result === 'success') status = closing ? 'Preview removed because this PR is closed.' :
    `**[Open site preview](${previewURL({preview: {urls: [url]}})})**\n\nCommit: \`${sha}\`\n\nThis URL updates with this PR. [Workflow run](${run}).`;
  const body = `${marker}\n${status}`;
  const params = {...context.repo, issue_number: context.payload.number};
  const comments = await github.paginate(github.rest.issues.listComments, {...params, per_page: 100});
  const existing = comments.find(c => c.user?.login === 'github-actions[bot]' && c.body?.startsWith(marker));
  if (existing) await github.rest.issues.updateComment({...context.repo, comment_id: existing.id, body});
  else if (!closing) await github.rest.issues.createComment({...params, body});
}
module.exports = {eligible, current, config, validateAssets, previewURL, deploy, comment};
