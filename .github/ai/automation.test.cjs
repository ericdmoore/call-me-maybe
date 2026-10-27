const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
const authorize = require('./authorize.cjs');
const {route, opencode} = require('./models.cjs');
const {LOCAL_MODEL, LOCAL_ALIAS} = require('./models.cjs');
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
    repos: {getCollaboratorPermissionLevel: async ({username}) => {
      options.lookups?.push(username);
      return {data: {permission: options.permission || 'write'}};
    }},
    users: {getByUsername: async () => ({data: {type: options.actorType || 'User'}})}},
    paginate: async () => options.history || []};
  await authorize({github, context: {repo: {owner: 'owner', repo: 'repo'}, payload: options.payload || {},
    eventName: 'issue_comment', ref: 'refs/heads/main', actor: 'dispatcher', runId: 42,
    serverUrl: 'https://github.com', ...options.context},
    core: {setOutput: (k,v) => outputs[k] = v},
    env: {MODE: 'repair', POLICY_SHA: 'trusted', DEFAULT_MODEL: 'ollama/gpt-oss:20b',
      PR_NUMBER: '12', COMMENT_ID: '99', COMMENT_KIND: 'issue', ...options.env}});
  return outputs;
}
const manual = (options = {}) => gate({...options,
  context: {eventName: 'workflow_dispatch', ...options.context},
  env: {COMMENT_KIND: 'manual', COMMENT_ID: '', RUN_ATTEMPT: '1', MANUAL_MODEL: 'default',
    REPAIR_REQUEST: 'Validate findings and fix confirmed issues.', ...options.env},
});
test('manual repair authenticates its dispatcher and claims exactly one run on the current head', async () => {
  const lookups = [];
  const out = await manual({lookups});
  assert.deepEqual(lookups, ['dispatcher']);
  assert.equal(out.model, LOCAL_MODEL);
  assert.equal(out.head, 'head');
  assert.equal(out.ref, 'refs/heads/feature');
  assert.equal(out.marker, '<!-- cmm-oc-request:manual:42 -->');
  assert.equal(out.source, 'https://github.com/owner/repo/actions/runs/42');
  assert.equal(out.request, 'Validate findings and fix confirmed issues.');
  await assert.rejects(manual({history: [{user: {login: 'github-actions[bot]'}, body: out.marker}]}));
  assert.equal((await manual({context: {runId: 43}})).marker, '<!-- cmm-oc-request:manual:43 -->');
});
test('manual picker uses only the repair default or the explicit selected model', async () => {
  const paid = 'openrouter/anthropic/claude-sonnet-5.5';
  assert.equal((await manual({env: {DEFAULT_MODEL: paid}})).model, paid);
  assert.equal((await manual({env: {MANUAL_MODEL: paid}})).model, paid);
  assert.equal((await manual({env: {DEFAULT_MODEL: paid, MANUAL_MODEL: 'ollama/gpt-oss:20b'}})).provider, 'litellm');
  assert.equal((await manual({env: {MANUAL_MODEL: 'openrouter/qwen/qwen3.8-27b:free'}})).model_id, 'qwen/qwen3.8-27b:free');
  await assert.rejects(manual({env: {MANUAL_MODEL: 'openrouter/auto'}}));
  await assert.rejects(manual({env: {MANUAL_MODEL: 'ollama/unknown'}}));
  // Relay inputs never override the original writer's explicit comment choice.
  await assert.rejects(gate({env: {COMMENT_KIND: 'inline', MANUAL_MODEL: paid}}));
  assert.equal((await gate({env: {COMMENT_KIND: 'inline', MANUAL_MODEL: 'default'},
    comment: {body: `/oc --model ${paid} fix`}})).model, paid);
});
test('manual repairs reject nonwriters, bots, forks, stale heads, reruns and non-main dispatches', async () => {
  for (const options of [
    {permission: 'read'}, {permission: 'triage'}, {permission: 'none'}, {actorType: 'Bot'},
    {context: {actor: ''}}, {context: {eventName: 'issue_comment'}},
    {context: {ref: 'refs/heads/feature'}}, {context: {runId: 0}},
    {env: {RUN_ATTEMPT: '2'}}, {env: {RUN_ATTEMPT: ''}}, {env: {COMMENT_ID: '99'}},
    {env: {EXPECTED_HEAD: 'old'}}, {env: {REPAIR_REQUEST: '  '}}, {env: {REPAIR_REQUEST: 'x'.repeat(8001)}},
    {pr: {draft: true}}, {pr: {state: 'closed'}},
    {pr: {head: {sha: 'head', ref: 'feature', repo: {full_name: 'outsider/repo'}}}},
  ]) await assert.rejects(manual(options));
  for (const permission of ['write', 'maintain', 'admin']) assert.equal((await manual({permission})).head, 'head');
});
test('manual review and repair menus have identical sorted choices and every choice routes', () => {
  const options = file => fs.readFileSync(path.join(__dirname, '../workflows', file), 'utf8')
    .split('        options:\n')[1].match(/^(?:          - .+\n)+/)[0]
    .trimEnd().split('\n').map(line => line.slice('          - '.length));
  const review = options('ocr-review.yml');
  const repair = options('opencode-repair.yml');
  assert.deepEqual(repair, review);
  assert.equal(repair[0], 'default');
  assert.deepEqual(repair.slice(1), repair.slice(1).sort());
  for (const model of repair.slice(1)) assert.ok(opencode(model).model);
});
test('writer gets local model and exact branch/head; maintain also allowed', async () => {
  assert.equal((await gate()).model, LOCAL_MODEL);
  assert.equal((await gate({permission: 'maintain'})).ref, 'refs/heads/feature');
});
test('the local name and both legacy Ollama ids route to the one LiteLLM alias', async () => {
  const current = await gate();
  for (const legacyId of ['ollama/gpt-oss:20b', 'ollama/bullmoose-ocr:20b']) {
    const legacy = await gate({env: {DEFAULT_MODEL: legacyId}});
    assert.equal(legacy.model, LOCAL_MODEL);
    assert.equal(legacy.model_id, current.model_id);
    assert.deepEqual(opencode(legacyId), opencode(LOCAL_MODEL));
  }
  assert.equal(current.provider, 'litellm');
  assert.equal(current.model_id, LOCAL_ALIAS);
  assert.equal(current.url, 'http://alpaca.local:4000/v1');
  const config = opencode(current.model);
  assert.equal(config.model, LOCAL_MODEL);
  assert.deepEqual(config.enabled_providers, ['litellm']);
  assert.equal(config.provider.litellm.models[LOCAL_ALIAS].id, LOCAL_ALIAS);
  assert.equal(config.provider.litellm.models[LOCAL_ALIAS].limit.context, 65536);
  // The key reaches OpenCode from the environment when it runs; the config never holds one.
  assert.equal(config.provider.litellm.options.apiKey, '{env:LITELLM_REVIEW_KEY}');
  assert.ok(!JSON.stringify(config).includes('sk-'));
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
  // Spellings Number() accepts but the raw-string concurrency key would not share with "12".
  for (const env of [{POLICY_SHA: ''}, {DEFAULT_MODEL: ''}, {PR_NUMBER: 'NaN'}, {PR_NUMBER: '012'}, {PR_NUMBER: ' 12'},
    {PR_NUMBER: '1e1'}, {PR_NUMBER: '12.0'}, {PR_NUMBER: '0x0c'}, {COMMENT_ID: '0'}, {COMMENT_KIND: 'bogus'}]) await assert.rejects(gate({env}));
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
  // OpenRouter's own routers and cost-adding variants are not exact model ids.
  for (const model of ['openrouter/openai/gpt-oss-20b/', 'auto', 'openai/gpt-4', 'ollama/unknown', 'openrouter/auto', 'openrouter/a/b\n',
    'openrouter/openrouter/auto', 'openrouter/openrouter/free', 'openrouter/anthropic/claude-sonnet-4.5:online',
    'openrouter/openai/gpt-oss-20b:nitro', 'openrouter/openai/gpt-oss-20b:free:online']) assert.throws(() => route(model));
  assert.equal(route('openrouter/qwen/qwen3.8-27b:free').model, 'qwen/qwen3.8-27b:free');
  assert.deepEqual(opencode('ollama/gpt-oss:20b').enabled_providers, ['litellm']);
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
test('repair workflow cleanup removes read-only Go caches without following external links', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-cleanup-'));
  const job = path.join(dir, 'job');
  const moduleDir = path.join(job, 'home/go/pkg/mod/example@v1');
  const outside = path.join(dir, 'outside');
  try {
    fs.mkdirSync(moduleDir, {recursive: true});
    fs.mkdirSync(outside);
    fs.writeFileSync(path.join(moduleDir, 'source.go'), 'package fixture\n', {mode: 0o444});
    fs.writeFileSync(path.join(outside, 'keep'), 'untouched');
    fs.symlinkSync(outside, path.join(job, 'external'));
    fs.chmodSync(moduleDir, 0o555);
    fs.chmodSync(outside, 0o555);
    // Run the actual workflow cleanup, with git stubbed so it cannot alter this checkout.
    fs.writeFileSync(path.join(dir, 'git'), '#!/bin/sh\nexit 0\n', {mode: 0o755});
    const workflow = fs.readFileSync(path.join(__dirname, '../workflows/opencode-repair.yml'), 'utf8');
    const cleanup = workflow.split('      - name: Remove job credentials and state\n')[1]
      .split('        run: |\n')[1].split('\n').map(line => line.replace(/^          /, '')).join('\n');
    const result = spawnSync('bash', ['-e', '-c', cleanup], {encoding: 'utf8', cwd: dir,
      env: {...process.env, AI_JOB_DIR: job, PATH: `${dir}:/usr/bin:/bin`}});
    assert.equal(result.status, 0, result.stderr);
    assert.equal(fs.existsSync(job), false);
    assert.equal(fs.readFileSync(path.join(outside, 'keep'), 'utf8'), 'untouched');
    assert.equal(fs.statSync(outside).mode & 0o777, 0o555);
  } finally {
    if (fs.existsSync(moduleDir)) fs.chmodSync(moduleDir, 0o755);
    fs.chmodSync(outside, 0o755);
    fs.rmSync(dir, {recursive: true, force: true});
  }
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
test('the local key comes from the host file, cloud needs its explicit secret, neither falls back', () => {
  const {credential} = require('./models.cjs');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cmm-key-'));
  const file = path.join(dir, 'litellm-review.key');
  fs.writeFileSync(file, 'sk-test-placeholder\n');
  assert.equal(credential('ollama/gpt-oss:20b', '', file), 'sk-test-placeholder');
  assert.equal(credential(LOCAL_MODEL, 'ignored-cloud-key', file), 'sk-test-placeholder');
  assert.throws(() => credential(LOCAL_MODEL, '', ''), /no fallback/);
  assert.throws(() => credential(LOCAL_MODEL, '', undefined), /no fallback/);
  assert.throws(() => credential(LOCAL_MODEL, '', 'relative.key'), /no fallback/);
  assert.throws(() => credential(LOCAL_MODEL, '', path.join(dir, 'missing.key')), /unreadable/);
  fs.writeFileSync(file, '\n');
  assert.throws(() => credential(LOCAL_MODEL, '', file), /empty/);
  assert.throws(() => credential('openrouter/openai/gpt-oss-20b', ''), /no fallback/);
  assert.equal(credential('openrouter/openai/gpt-oss-20b', 'test-placeholder'), 'test-placeholder');
});
test('zero CLI exit, provider errors and an empty stop are not repair completion', () => {
  const verify = require('./repair-result.cjs');
  const stop = {type: 'step_finish', part: {reason: 'stop'}};
  const assessment = {type: 'text', part: {text: 'Invalid finding: concrete evidence.'}};
  assert.throws(() => verify(JSON.stringify(stop)));
  assert.throws(() => verify([stop, assessment, {type: 'error'}].map(JSON.stringify).join('\n')));
  assert.doesNotThrow(() => verify([assessment, stop].map(JSON.stringify).join('\n')));
});
test('host guard rejects fork events and PR-defined workflows before job steps', () => {
  const source = `
import importlib.util
spec = importlib.util.spec_from_file_location('guard', ${JSON.stringify(path.join(__dirname, 'runner-guard.py'))})
g = importlib.util.module_from_spec(spec)
spec.loader.exec_module(g)
e = {'GITHUB_REPOSITORY': g.REPOSITORY, 'GITHUB_EVENT_NAME': 'workflow_dispatch',
     'GITHUB_WORKFLOW_REF': g.REPOSITORY + '/.github/workflows/ocr-review.yml@refs/heads/main'}
assert g.allowed(e, {})
assert not g.allowed(dict(e, GITHUB_EVENT_NAME='pull_request'), {})
assert not g.allowed(dict(e, GITHUB_WORKFLOW_REF=e['GITHUB_WORKFLOW_REF'].replace('refs/heads/main','refs/pull/12/merge')), {})
e['GITHUB_EVENT_NAME'] = 'pull_request_target'
pr = {'state': 'open', 'draft': False, 'head': {'repo': {'full_name': 'outsider/repo'}}}
assert not g.allowed(e, {'pull_request': pr})
pr['head']['repo']['full_name'] = g.REPOSITORY
assert g.allowed(e, {'pull_request': pr})
e['GITHUB_WORKFLOW_REF'] = g.REPOSITORY + '/.github/workflows/site-preview.yml@refs/heads/main'
pr['base'] = {'ref': 'main'}
for action in ['opened', 'synchronize', 'reopened']:
    assert g.allowed(e, {'action': action, 'pull_request': pr})
for action in ['edited', 'closed']:
    assert not g.allowed(e, {'action': action, 'pull_request': pr})
assert not g.allowed(dict(e, GITHUB_EVENT_NAME='pull_request'), {'action': 'opened', 'pull_request': pr})
assert not g.allowed(e, {'action': 'opened', 'pull_request': dict(pr, base={'ref': 'other'})})
# Cleanup admits only the close event, whatever the PR's final base branch.
c = dict(e, GITHUB_WORKFLOW_REF=g.REPOSITORY + '/.github/workflows/site-preview-cleanup.yml@refs/heads/main')
assert g.allowed(c, {'action': 'closed', 'pull_request': pr})
assert g.allowed(c, {'action': 'closed', 'pull_request': dict(pr, base={'ref': 'other'})})
for action in ['opened', 'synchronize', 'reopened', 'edited']:
    assert not g.allowed(c, {'action': action, 'pull_request': pr})
assert not g.allowed(dict(c, GITHUB_EVENT_NAME='pull_request'), {'action': 'closed', 'pull_request': pr})
pr['head']['repo']['full_name'] = 'outsider/repo'
assert not g.allowed(e, {'action': 'opened', 'pull_request': pr})
assert not g.allowed(c, {'action': 'closed', 'pull_request': pr})
# The payload shapes GitHub really sends for a deleted fork or a non-PR event
# make allowed() raise; the decision must still be a refusal, not a crash.
import json, tempfile
for payload in [{'action': 'opened', 'pull_request': {'head': {'repo': None}, 'base': {'ref': 'main'}}},
                {'action': 'opened', 'pull_request': None}, []]:
    with tempfile.NamedTemporaryFile('w', suffix='.json', delete=False) as f:
        json.dump(payload, f)
    assert g.decide(e, f.name) is False, payload
assert g.decide(e, '/nonexistent/event.json') is False
assert g.decide(dict(e, GITHUB_REPOSITORY='outsider/repo'), f.name) is False
with tempfile.NamedTemporaryFile('w', suffix='.json', delete=False) as f:
    json.dump({'action': 'opened', 'pull_request': {'state': 'open', 'draft': False, 'base': {'ref': 'main'}, 'head': {'repo': {'full_name': g.REPOSITORY}}}}, f)
assert g.decide(e, f.name) is True
`;
  const result = spawnSync('python3', ['-I', '-c', source], {encoding: 'utf8'});
  assert.equal(result.status, 0, result.stderr);
});
