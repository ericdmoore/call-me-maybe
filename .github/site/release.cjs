const fs = require('node:fs');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const {wranglerPath, validateAssets} = require('./preview.cjs');

// Wrangler's config is JSON with comments. Strip them outside strings only,
// so a `//` inside a value survives, and leave the rest to JSON.parse: a
// trailing comma or a typo fails loudly rather than being guessed at.
function stripComments(text) {
  let out = '';
  for (let i = 0, inString = false; i < text.length; i++) {
    const ch = text[i];
    if (inString) {
      out += ch;
      if (ch === '\\') out += text[++i] ?? '';
      else if (ch === '"') inString = false;
    } else if (ch === '"') { inString = true; out += ch; }
    else if (ch === '/' && text[i + 1] === '/') { while (i < text.length && text[i] !== '\n') i++; out += '\n'; }
    else if (ch === '/' && text[i + 1] === '*') { const end = text.indexOf('*/', i + 2); i = end < 0 ? text.length : end + 1; }
    else out += ch;
  }
  return out;
}
// The production hostname is whichever custom domain the committed
// site/wrangler.jsonc attaches first. That file is the one source of the
// routes, so reading it here means the probe can never drift from the deploy,
// and a config that attaches no domain has no production to verify.
function siteURL(configFile) {
  let config;
  try {
    config = JSON.parse(stripComments(fs.readFileSync(configFile, 'utf8')));
  } catch (error) {
    throw new Error(`Cannot read the routes from ${configFile}: ${error.message}`);
  }
  const routes = Array.isArray(config.routes) ? config.routes : [];
  const route = routes.find(r => r?.custom_domain === true && typeof r.pattern === 'string' && r.pattern.trim());
  if (!route) throw new Error(`${configFile} attaches no custom domain, so there is no production hostname to verify`);
  return `https://${route.pattern.trim()}/`;
}
/**
 * Whether this run may publish production.
 * @param {string} ref the fully qualified git ref the workflow ran for
 * @param {string} eventName the GitHub event that started the run
 * @returns {boolean} true only for main itself, pushed or dispatched by hand:
 *   not a tag, not another branch, not a PR merge ref, not a PR event.
 */
function publishable(ref, eventName) {
  return ref === 'refs/heads/main' && (eventName === 'push' || eventName === 'workflow_dispatch');
}
// The workflow passes these; a run without them should say which is missing
// rather than fail inside path.resolve with no name attached.
function required(name, env = process.env) {
  const value = env[name];
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${name} is not set; the workflow passes it to this step`);
  return value;
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
    throw new Error(`Refusing to publish ${context.ref} on ${context.eventName}: only main is published`);
  const siteDir = path.resolve(required('SITE_DIR'));
  const tmpDir = required('RUNNER_TEMP');
  const configFile = path.join(siteDir, 'wrangler.jsonc');
  const site = siteURL(configFile);
  const dist = path.join(siteDir, 'dist');
  if (!fs.statSync(dist, {throwIfNoEntry: false})?.isDirectory())
    throw new Error(`Missing ${dist}: the build artifact was not downloaded`);
  // The artifact is static data and nothing else; a symlink in it is a leak, not a page.
  validateAssets(dist);
  const indexFile = path.join(dist, 'index.html');
  if (!fs.statSync(indexFile, {throwIfNoEntry: false})?.isFile()) throw new Error('Missing index.html');
  const expected = fs.readFileSync(indexFile);
  const outputFile = path.join(tmpDir, `cmm-site-release-${process.pid}.jsonl`);
  // The committed wrangler.jsonc is the one source of the routes, so production
  // uses it as-is; --config names it explicitly rather than trusting cwd lookup.
  execFileSync(wranglerPath(), ['deploy', '--config', configFile], {cwd: siteDir, encoding: 'utf8',
    stdio: ['ignore', 'inherit', 'inherit'], timeout: 180000,
    env: {...process.env, CI: 'true', WRANGLER_SEND_METRICS: 'false', WRANGLER_OUTPUT_FILE_PATH: outputFile}});
  const records = fs.readFileSync(outputFile, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  const {version, targets} = deployRecord(records);
  await served(site, expected, options);
  core.setOutput('version', version);
  core.notice(`Published version ${version} of ${context.sha} to ${targets.join(', ') || site}`);
}
module.exports = {stripComments, siteURL, publishable, deployRecord, served, release};
