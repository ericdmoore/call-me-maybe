const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
const authorize = require('./authorize.cjs');
const {route, opencode} = require('./models.cjs');
const {isolatedEnv} = require('./isolate.cjs');
async function gate(options = {}) {
  const outputs = {};
  const pr = {number: 12, state: 'open', draft: false, base: {sha: 'base'},
    head: {sha: 'head', ref: 'feature', repo: {full_name: 'owner/repo'}}, ...options.pr};
  const comment = {id: 99, body: '/oc fix findings', user: {type: 'User', login: 'writer'},
    issue_url: 'https://api.github.com/repos/owner/repo/issues/12',
    pull_request_url: 'https://api.github.com/repos/owner/repo/pulls/12', commit_id: 'head', ...options.comment};
  const github = {rest: {pulls: {get: async () => ({data: pr}), getReviewComment: async () => ({data: comment})},
    issues: {getComment: async () => ({data: comment}), listComments: () => {}},
    repos: {getCollaboratorPermissionLevel: async () => ({data: {permission: options.permission || 'write'}})}},
    paginate: async () => options.history || []};
  await authorize({github, context: {repo: {owner: 'owner', repo: 'repo'}, payload: options.payload || {}},
    core: {setOutput: (k,v) => outputs[k] = v},
    env: {MODE: 'repair', POLICY_SHA: 'trusted', DEFAULT_MODEL: 'ollama/bullmoose-ocr:20b',
      PR_NUMBER: '12', COMMENT_ID: '99', COMMENT_KIND: 'issue', ...options.env}});
  return outputs;
}
test('writer gets local model and exact branch/head; maintain also allowed', async () => {
  assert.equal((await gate()).model, 'ollama/bullmoose-ocr:20b');
  assert.equal((await gate({permission: 'maintain'})).ref, 'refs/heads/feature');
});
test('deny read/triage, bots, issue-only comments, drafts, closed PRs, forks', async () => {
  for (const o of [{permission: 'read'}, {permission: 'triage'}, {comment: {user: {type: 'Bot'}}},
    {comment: {body: 'please /oc'}}, {comment: {issue_url: 'https://api.github.com/issues/3'}},
    {pr: {draft: true}}, {pr: {state: 'closed'}},
    {pr: {head: {sha: 'head', repo: {full_name: 'outsider/repo'}}}}]) await assert.rejects(gate(o));
});
test('reject stale inline request, changed event and expected SHA', async () => {
  await assert.rejects(gate({env: {COMMENT_KIND: 'inline'}, comment: {commit_id: 'old'}}));
  await assert.rejects(gate({payload: {pull_request: {head: {sha: 'old'}}}}));
  await assert.rejects(gate({env: {EXPECTED_HEAD: 'old'}}));
  assert.equal((await gate({env: {COMMENT_KIND: 'inline'}})).head, 'head');
});
test('fail closed on missing config, invalid numbers and incomplete override', async () => {
  for (const env of [{POLICY_SHA: ''}, {DEFAULT_MODEL: ''}, {PR_NUMBER: 'NaN'}, {COMMENT_ID: '0'}, {COMMENT_KIND: 'bogus'}]) await assert.rejects(gate({env}));
  await assert.rejects(gate({comment: {body: '/oc --model'}}));
});
test('each bot-claimed request has one attempt, a forged user marker does not consume it', async () => {
  const body = '<!-- cmm-oc-request:issue:99 -->';
  await assert.rejects(gate({history: [{user: {login: 'github-actions[bot]'}, body}]}));
  assert.equal((await gate({history: [{user: {login: 'outsider'}, body}]})).number, 12);
});
test('exact OpenRouter override is opt-in; no other provider or malformed ID', async () => {
  const o = await gate({comment: {body: '/oc --model openrouter/openai/gpt-oss-20b fix'}});
  assert.equal(o.model_id, 'openai/gpt-oss-20b');
  for (const model of ['openrouter/openai/gpt-oss-20b/', 'auto', 'openai/gpt-4', 'ollama/unknown', 'openrouter/auto', 'openrouter/a/b\n']) assert.throws(() => route(model));
  assert.deepEqual(opencode('ollama/bullmoose-ocr:20b').enabled_providers, ['ollama']);
  assert.equal(opencode(o.model).small_model, o.model);
});
test('child config and authentication directories are isolated without mutating parent HOME', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-isolate-'));
  try {
    const original = process.env.HOME;
    const env = isolatedEnv(root);
    assert.notEqual(env.HOME, original);
    assert.equal(process.env.HOME, original);
    assert.ok(env.GH_CONFIG_DIR.startsWith(root));
    assert.equal(env.OCR_ENABLE_TELEMETRY, 'false');
    assert.equal(env.OPENCODE_DISABLE_PROJECT_CONFIG, 'true');
  } finally {fs.rmSync(root, {recursive: true, force: true});}
});
test('push hook rejects stale/wrong/multiple refs and propagates validation failures', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-hook-'));
  const hook = path.join(__dirname, 'pre-push');
  try {
    fs.writeFileSync(path.join(dir, 'git'), '#!/bin/sh\nexit 0\n', {mode: 0o755});
    fs.writeFileSync(path.join(dir, 'validate.sh'), '#!/bin/sh\nexit 23\n', {mode: 0o755});
    const run = (input, extra = {}) => spawnSync('bash', [hook], {input, encoding: 'utf8',
      env: {...process.env, PATH: `${dir}:/usr/bin:/bin`, AI_POLICY: dir, REPAIR_BASE_SHA: 'base',
        REPAIR_HEAD_REF: 'refs/heads/feature', REPAIR_HEAD_SHA: 'old', ...extra}});
    for (const input of ['', 'local new refs/heads/main old\n', 'local new refs/heads/feature changed\n',
      'local new refs/heads/feature old\nlocal new refs/heads/feature old\n']) assert.equal(run(input).status, 1);
    assert.equal(run('local new refs/heads/feature old\n').status, 23);
    assert.equal(run('local new refs/heads/feature old\n', {REPAIR_HEAD_SHA: ''}).status, 1);
    fs.writeFileSync(path.join(dir, 'validate.sh'), '#!/bin/sh\nexit 0\n');
    assert.equal(run('local new refs/heads/feature old\n').status, 1, 'remote changed while tests ran');
  } finally {fs.rmSync(dir, {recursive: true, force: true});}
});
test('zero findings cannot hide incomplete or missing coverage', () => {
  const coverage = require('./coverage.cjs');
  assert.match(coverage({comments: []}), /Coverage unknown/);
  const result = {comments: [], manifest: {schema_version: 'ocr.run-manifest/v1',
    input: {resolved_head: 'head'}, terminal_state: 'partial', coverage: {
      selected: [{path: 'a.go'}], completed: [], failed: [{path: 'a.go', classification: 'budget'}], waived: []}}};
  assert.match(coverage(result), /Incomplete.*1 failed/);
  assert.match(coverage(result), /a.go \(budget\)/);
});
test('cloud credentials are mandatory only for an explicit cloud route', () => {
  const {credential} = require('./models.cjs');
  assert.equal(credential('ollama/bullmoose-ocr:20b', ''), 'ollama');
  assert.throws(() => credential('openrouter/openai/gpt-oss-20b', ''), /no fallback/);
  assert.equal(credential('openrouter/openai/gpt-oss-20b', 'test-placeholder'), 'test-placeholder');
});
