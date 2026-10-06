const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {stripComments, siteURL, publishable, deployRecord, served, release} = require('./release.cjs');

test('the production hostname is the first custom domain the committed config attaches', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-release-'));
  const file = path.join(dir, 'wrangler.jsonc');
  fs.writeFileSync(file, `{
    // a comment with a "quote" and a url https://example.test/
    "name": "callmemaybe", /* a block
    comment */ "slash": "a//b",
    "routes": [
      { "pattern": "callmemaybe.workers.dev", "custom_domain": false },
      { "pattern": "example.test", "custom_domain": true },
      { "pattern": "www.example.test", "custom_domain": true }
    ]
  }`);
  assert.equal(siteURL(file), 'https://example.test/');
  assert.equal(JSON.parse(stripComments(fs.readFileSync(file, 'utf8'))).slash, 'a//b');
  fs.writeFileSync(file, '{"routes": [{"pattern": "only.workers.dev", "custom_domain": false}]}');
  assert.throws(() => siteURL(file), /no custom domain/);
  fs.writeFileSync(file, '{"routes": [,]}');
  assert.throws(() => siteURL(file), /Cannot read the routes/);
  assert.throws(() => siteURL(path.join(dir, 'missing.jsonc')), /Cannot read the routes/);
  // The committed file, so the hostname the docs promise is the one probed.
  assert.equal(siteURL(path.join(__dirname, '../../site/wrangler.jsonc')), 'https://callmemaybe.cc/');
});
test('release names what is missing instead of crashing on it', async () => {
  const core = {setOutput: () => assert.fail('published'), notice: () => assert.fail('published')};
  const context = {ref: 'refs/heads/main', eventName: 'push'};
  const saved = {SITE_DIR: process.env.SITE_DIR, RUNNER_TEMP: process.env.RUNNER_TEMP};
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-release-'));
  try {
    delete process.env.SITE_DIR;
    delete process.env.RUNNER_TEMP;
    await assert.rejects(release({context, core}), /SITE_DIR is not set/);
    process.env.SITE_DIR = dir;
    await assert.rejects(release({context, core}), /RUNNER_TEMP is not set/);
    process.env.RUNNER_TEMP = dir;
    await assert.rejects(release({context, core}), /Cannot read the routes/);
    fs.writeFileSync(path.join(dir, 'wrangler.jsonc'), '{"routes": [{"pattern": "example.test", "custom_domain": true}]}');
    await assert.rejects(release({context, core}), /build artifact was not downloaded/);
    fs.mkdirSync(path.join(dir, 'dist'));
    await assert.rejects(release({context, core}), /Missing index\.html/);
  } finally {
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[name]; else process.env[name] = value;
    }
  }
});

test('only main itself publishes, by push or by hand', () => {
  assert.ok(publishable('refs/heads/main', 'push'));
  assert.ok(publishable('refs/heads/main', 'workflow_dispatch'));
  assert.ok(!publishable('refs/heads/main', 'pull_request'));
  assert.ok(!publishable('refs/heads/main', 'pull_request_target'));
  assert.ok(!publishable('refs/heads/feature', 'push'));
  assert.ok(!publishable('refs/tags/v1.0.0', 'push'));
  assert.ok(!publishable('refs/pull/12/merge', 'push'));
});
test('the deployment record is the last deploy row and must name a version', () => {
  assert.throws(() => deployRecord([]), /no deployment/);
  assert.throws(() => deployRecord([{type: 'preview', version_id: 'x'}]), /no deployment/);
  assert.throws(() => deployRecord([{type: 'deploy', version_id: ''}]), /no deployment/);
  assert.throws(() => deployRecord([{type: 'deploy'}]), /no deployment/);
  assert.deepEqual(deployRecord([
    {type: 'deploy', version_id: 'old', targets: ['a']},
    {type: 'wrangler-session'},
    {type: 'deploy', version_id: 'new', targets: ['callmemaybe.cc', 'www.callmemaybe.cc']},
  ]), {version: 'new', targets: ['callmemaybe.cc', 'www.callmemaybe.cc']});
  assert.deepEqual(deployRecord([{type: 'deploy', version_id: 'v'}]), {version: 'v', targets: []});
});
test('the probe accepts only this build, and waits for propagation', async () => {
  const expected = Buffer.from('<html>new</html>');
  const response = (status, body) => ({status, arrayBuffer: async () => Buffer.from(body)});
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({url, init});
    return calls.length < 3 ? response(200, '<html>old</html>') : response(200, '<html>new</html>');
  };
  await served('https://example.test/', expected, {fetch, attempts: 4, delay: 0});
  assert.equal(calls.length, 3);
  assert.equal(calls[0].init.headers['cache-control'], 'no-cache');
  assert.equal(calls[0].init.redirect, 'manual');
  await assert.rejects(served('https://example.test/', expected,
    {fetch: async () => response(200, '<html>old</html>'), attempts: 2, delay: 0}), /different index\.html/);
  await assert.rejects(served('https://example.test/', expected,
    {fetch: async () => response(301, ''), attempts: 2, delay: 0}), /HTTP 301/);
  await assert.rejects(served('https://example.test/', expected,
    {fetch: async () => { throw new Error('ECONNRESET'); }, attempts: 2, delay: 0}), /ECONNRESET/);
});
test('release refuses anything but main before touching the site', async () => {
  const core = {setOutput: () => assert.fail('published'), notice: () => assert.fail('published')};
  for (const context of [{ref: 'refs/heads/feature', eventName: 'push'},
    {ref: 'refs/heads/main', eventName: 'pull_request_target'}, {ref: 'refs/tags/v1', eventName: 'push'}])
    await assert.rejects(release({context, core}), /Refusing to publish/);
});
