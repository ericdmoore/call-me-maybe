const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const handoff = require('./repair-handoff.cjs');
const before = 'a'.repeat(40), pushed = 'b'.repeat(40), other = 'c'.repeat(40);

function fixture(snapshots = [{}], options = {}) {
  const sleeps = [], calls = [], messages = [];
  let reads = 0;
  const getSnapshot = () => snapshots[Math.min(reads - 1, snapshots.length - 1)];
  const args = {
    sha: pushed, context: {repo: {owner: 'owner', repo: 'repo'}},
    env: {PR_NUMBER: '40', REPAIR_HEAD_SHA: before, REPAIR_HEAD_REF: 'refs/heads/feature'},
    core: {info: line => messages.push(line)}, sleep: async ms => sleeps.push(ms),
    github: {rest: {
      pulls: {get: async request => {
        calls.push(['pr', request]); reads++;
        const snap = getSnapshot();
        return {data: {number: 40, state: 'open', draft: false,
          head: {sha: snap.pr || pushed, ref: 'feature', repo: {full_name: 'owner/repo'}}, ...options.pr}};
      }},
      git: {getRef: async request => {
        calls.push(['ref', request]);
        return {data: {object: {sha: getSnapshot().ref || pushed}}};
      }},
    }},
  };
  return {args, sleeps, calls, messages};
}

test('successful push hands off immediately when both API views agree', async () => {
  const f = fixture();
  assert.equal((await handoff(f.args)).head.sha, pushed);
  assert.deepEqual(f.sleeps, []);
  assert.deepEqual(f.calls, [
    ['pr', {owner: 'owner', repo: 'repo', pull_number: 40}],
    ['ref', {owner: 'owner', repo: 'repo', ref: 'heads/feature'}],
  ]);
});

test('pre-repair API snapshots can catch up without repeating a repair or accepting stale coverage', async () => {
  for (const lag of [{pr: before}, {ref: before}, {pr: before, ref: before}]) {
    const f = fixture([lag, lag, {}]);
    assert.equal((await handoff(f.args)).head.sha, pushed);
    assert.deepEqual(f.sleeps, [2000, 2000]);
    assert.equal(f.messages.length, 2);
  }
});

test('an actual newer commit in either view fails immediately', async () => {
  for (const changed of [{pr: other}, {ref: other}, {pr: other, ref: other}]) {
    const f = fixture([changed]);
    await assert.rejects(handoff(f.args), /Head changed after repair push/);
    assert.deepEqual(f.sleeps, []);
  }
  const f = fixture([{pr: before}, {ref: other}]);
  await assert.rejects(handoff(f.args), /Head changed after repair push/);
  assert.deepEqual(f.sleeps, [2000]);
});

test('persistent old metadata exhausts a bounded wait and does not claim success', async () => {
  const f = fixture([{pr: before}]);
  await assert.rejects(handoff(f.args), /follow-up validation was not queued/);
  assert.equal(f.calls.length, 12);
  assert.deepEqual(f.sleeps, Array(5).fill(2000));
});

test('a closed, draft, forked or retargeted PR cannot receive follow-up validation', async () => {
  for (const pr of [{state: 'closed'}, {draft: true},
    {head: {sha: pushed, ref: 'feature', repo: {full_name: 'other/repo'}}},
    {head: {sha: pushed, ref: 'different', repo: {full_name: 'owner/repo'}}}]) {
    const f = fixture([{}], {pr});
    await assert.rejects(handoff(f.args), /no longer eligible/);
    assert.deepEqual(f.sleeps, []);
  }
});

test('missing or inconsistent push identity fails before API reads', async () => {
  for (const changes of [{sha: ''}, {sha: before}, {env: {PR_NUMBER: 'NaN'}},
    {env: {REPAIR_HEAD_SHA: ''}}, {env: {REPAIR_HEAD_REF: 'refs/tags/feature'}}]) {
    const f = fixture();
    await assert.rejects(handoff({...f.args, ...changes, env: {...f.args.env, ...changes.env}}), /Invalid repair/);
    assert.deepEqual(f.calls, []);
  }
});

test('the workflow gates both dispatches and its success comment on the trusted handoff', async () => {
  const source = fs.readFileSync(path.join(__dirname, '../workflows/opencode-repair.yml'), 'utf8')
    .split('      - name: Explicit CI and cheap review for the pushed SHA\n')[1]
    .split('          script: |\n')[1].split('      - name:')[0]
    .split('\n').map(line => line.replace(/^            /, '')).join('\n');
  const run = new (Object.getPrototypeOf(async function () {}).constructor)('require', 'process', 'github', 'context', 'core', source);
  for (const mode of ['success', 'stale', 'no-push']) {
    const calls = [];
    const fakeRequire = name => {
      if (name === 'fs') return {existsSync: () => mode !== 'no-push', readFileSync: () => pushed + '\n'};
      assert.equal(name, '/trusted/repair-handoff.cjs');
      return async args => {
        calls.push(['handoff', args.sha]);
        if (mode === 'stale') throw new Error('Head changed');
        return {number: 40, head: {sha: pushed, ref: 'feature'}};
      };
    };
    const github = {rest: {
      actions: {createWorkflowDispatch: async args => calls.push(['dispatch', args])},
      issues: {createComment: async args => calls.push(['comment', args])},
    }};
    const context = {repo: {owner: 'owner', repo: 'repo'}, payload: {repository: {default_branch: 'main'}}};
    const core = {summary: {addRaw: () => ({write: async () => calls.push(['no-push'])})}};
    const result = run(fakeRequire, {env: {AI_JOB_DIR: '/job', AI_POLICY: '/trusted'}}, github, context, core);
    if (mode === 'stale') {
      await assert.rejects(result, /Head changed/);
      assert.deepEqual(calls, [['handoff', pushed]]);
    } else {
      await result;
      if (mode === 'no-push') assert.deepEqual(calls, [['no-push']]);
      else {
        assert.deepEqual(calls.slice(0, 3), [
          ['handoff', pushed],
          ['dispatch', {...context.repo, workflow_id: 'ci.yml', ref: 'feature', inputs: {expected_head: pushed}}],
          ['dispatch', {...context.repo, workflow_id: 'ocr-review.yml', ref: 'main', inputs: {pr: '40', head: pushed, cheap: 'true'}}],
        ]);
        assert.equal(calls[3][0], 'comment');
      }
    }
  }
});
