const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {eligible, config, validateAssets, previewURL, comment} = require('./preview.cjs');
const pr = {head: {sha: 'abc', repo: {full_name: 'owner/repo'}}, base: {ref: 'main'}, state: 'open'};
test('only the current same-repo PR can publish; close is state checked', () => {
  assert.ok(eligible(pr, 'owner/repo', 'abc', false));
  assert.ok(!eligible(pr, 'other/repo', 'abc', false));
  assert.ok(!eligible(pr, 'owner/repo', 'old', false));
  assert.ok(!eligible(pr, 'owner/repo', 'abc', true));
  assert.ok(!eligible({...pr, base: {ref: 'other'}}, 'owner/repo', 'abc', false));
  assert.ok(eligible({...pr, state: 'closed'}, 'owner/repo', 'abc', true));
});
test('upload config has only static assets, no production routes or PR build hooks', () => {
  assert.deepEqual(Object.keys(config('./dist')).sort(),
    ['account_id', 'assets', 'compatibility_date', 'name', 'previews', 'workers_dev']);
});
test('reject symlink assets and non-Cloudflare result URLs', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-preview-test-'));
  try {
    fs.writeFileSync(path.join(dir, 'index.html'), 'hello');
    validateAssets(dir);
    fs.symlinkSync('/etc/passwd', path.join(dir, 'leak'));
    assert.throws(() => validateAssets(dir), /Non-regular/);
  } finally { fs.rmSync(dir, {recursive: true}); }
  for (const url of ['https://evil.test/', 'http://foo.workers.dev/', 'https://u:p@foo.workers.dev/'])
    assert.throws(() => previewURL({preview: {urls: [url]}}));
});
test('updates only its bot-owned comment and skips stale results', async () => {
  const writes = [];
  let current = structuredClone(pr);
  const github = {rest: {pulls: {get: async () => ({data: current})}, issues: {
    listComments: {}, updateComment: async x => writes.push(x), createComment: async () => assert.fail('duplicate'),
  }}, paginate: async () => [
    {id: 1, user: {login: 'someone'}, body: '<!-- cmm-site-preview -->'},
    {id: 2, user: {login: 'github-actions[bot]'}, body: '<!-- another-bot-comment -->'},
    {id: 3, user: {login: 'github-actions[bot]'}, body: '<!-- cmm-site-preview -->'},
  ]};
  const context = {repo: {owner: 'owner', repo: 'repo'}, payload: {number: 1, action: 'synchronize',
    pull_request: {...pr, number: 1}}, serverUrl: 'https://github.com', runId: 9};
  await comment({github, context}, 'success', 'https://pr-1-test.example.workers.dev');
  assert.equal(writes[0].comment_id, 3);
  assert.match(writes[0].body, /Open site preview/);
  await comment({github, context}, 'failure', '');
  assert.match(writes[1].body, /Preview failed/);
  current.head.sha = 'new';
  await comment({github, context}, 'success', 'https://pr-1-test.example.workers.dev');
  assert.equal(writes.length, 2);
});

test('Wrangler resolves a standalone host link or an explicit absolute override', () => {
  const {wranglerPath} = require('./preview.cjs');
  assert.equal(wranglerPath({}, '/home/runner'), '/home/runner/.local/bin/wrangler');
  assert.equal(wranglerPath({WRANGLER_BIN: '/opt/tools/wrangler'}), '/opt/tools/wrangler');
  assert.throws(() => wranglerPath({WRANGLER_BIN: './node_modules/.bin/wrangler'}), /absolute host-managed/);
});
test('missing and malformed deployment URLs fail with actionable errors', () => {
  for (const output of [undefined, {}, {preview: {}}, {preview: {urls: []}},
    {preview: {urls: ['']}}, {preview: {urls: [null]}}, {preview: {urls: 'https://x.workers.dev'}}])
    assert.throws(() => previewURL(output), /Cloudflare returned no preview URL/);
  assert.throws(() => previewURL({preview: {urls: ['broken']}}), /invalid preview URL/);
});
test('successful jobs without usable URLs still publish a failure comment', async () => {
  const writes = [];
  const github = {rest: {pulls: {get: async () => ({data: pr})}, issues: {
    listComments: {}, createComment: async x => writes.push(x),
  }}, paginate: async () => []};
  const context = {repo: {owner: 'owner', repo: 'repo'}, payload: {number: 1,
    action: 'synchronize', pull_request: {...pr, number: 1}},
    serverUrl: 'https://github.com', runId: 9};
  for (const url of [undefined, '', 'broken', 'https://evil.test/']) {
    await comment({github, context}, 'success', url);
    assert.match(writes.at(-1).body, /Preview failed.*\n?/);
    assert.match(writes.at(-1).body, /did not provide a valid preview URL/);
    assert.doesNotMatch(writes.at(-1).body, /Open site preview/);
  }
  assert.equal(writes.length, 4);
});
