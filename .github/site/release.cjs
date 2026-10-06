const fs = require('node:fs');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const {wranglerPath, validateAssets} = require('./preview.cjs');

// The hostname site/wrangler.jsonc attaches. The probe at the end proves it
// serves the build this run made, not whichever one it served before.
const site = 'https://callmemaybe.cc/';

function publishable(ref, eventName) {
  // Production is main and nothing else: not a tag, not a branch, not a PR merge ref.
  return ref === 'refs/heads/main' && (eventName === 'push' || eventName === 'workflow_dispatch');
}
function deployRecord(records) {
  const row = records.findLast(record => record?.type === 'deploy');
  if (!row || typeof row.version_id !== 'string' || !row.version_id.trim())
    throw new Error('Wrangler reported no deployment');
  return {version: row.version_id, targets: Array.isArray(row.targets) ? row.targets : []};
}
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function served(url, expected, {fetch = globalThis.fetch, attempts = 8, delay = 5000} = {}) {
  // A deploy that reports success against a hostname still serving the previous
  // build is the failure this workflow exists to end, so compare bytes, not status.
  let last = 'no response';
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (attempt) await sleep(delay);
    try {
      const response = await fetch(url, {headers: {'cache-control': 'no-cache'}, redirect: 'manual'});
      const body = Buffer.from(await response.arrayBuffer());
      if (response.status === 200 && body.equals(expected)) return;
      last = response.status === 200 ? 'served a different index.html' : `HTTP ${response.status}`;
    } catch (error) {
      last = error.message;
    }
  }
  throw new Error(`${url} does not serve this build after ${attempts} attempts: ${last}`);
}
async function release({context, core}, options = {}) {
  if (!publishable(context.ref, context.eventName))
    throw new Error(`Refusing to publish ${context.ref} on ${context.eventName}: only main reaches ${site}`);
  const siteDir = path.resolve(process.env.SITE_DIR);
  const dist = path.join(siteDir, 'dist');
  // The artifact is static data and nothing else; a symlink in it is a leak, not a page.
  validateAssets(dist);
  const indexFile = path.join(dist, 'index.html');
  if (!fs.statSync(indexFile).isFile()) throw new Error('Missing index.html');
  const expected = fs.readFileSync(indexFile);
  const outputFile = path.join(process.env.RUNNER_TEMP, `cmm-site-release-${process.pid}.jsonl`);
  // The committed wrangler.jsonc is the one source of the routes, so production
  // uses it as-is; --config names it explicitly rather than trusting cwd lookup.
  execFileSync(wranglerPath(), ['deploy', '--config', 'wrangler.jsonc'], {cwd: siteDir, encoding: 'utf8',
    stdio: ['ignore', 'inherit', 'inherit'], timeout: 180000,
    env: {...process.env, CI: 'true', WRANGLER_SEND_METRICS: 'false', WRANGLER_OUTPUT_FILE_PATH: outputFile}});
  const records = fs.readFileSync(outputFile, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  const {version, targets} = deployRecord(records);
  await served(site, expected, options);
  core.setOutput('version', version);
  core.notice(`Published version ${version} of ${context.sha} to ${targets.join(', ') || site}`);
}
module.exports = {site, publishable, deployRecord, served, release};
