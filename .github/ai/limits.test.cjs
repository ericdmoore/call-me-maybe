const {test} = require('node:test');
const assert = require('node:assert/strict');
const {reviewLimits, requireRepairBudget} = require('./limits.cjs');
const {opencode} = require('./models.cjs');
const local = 'ollama/gpt-oss:20b';
const paid = 'openrouter/openai/gpt-oss-20b';

test('local review and re-review spend no token allowance and retain clock/context bounds', () => {
  for (const cheap of [false, true]) {
    const limits = reviewLimits(local, cheap, {OCR_PAID_REVIEW_TOKENS: 'broken', OCR_PAID_REREVIEW_TOKENS: '0'});
    assert.equal(limits.tokens, 0);
    assert.equal(limits.tools, 2147483647);
    assert.equal(limits.taskMinutes, 0);
    assert.equal(limits.minutes, cheap ? 20 : 80);
    assert.equal(limits.jobMinutes, limits.minutes + 10);
  }
  const config = opencode(local);
  assert.equal(config.agent?.build?.steps, undefined, 'local repairs have no configured step cap');
  assert.equal(config.provider.litellm.models['@local/openai/gpt-oss:20b-64k'].limit.context, 65536);
});

test('paid reviews keep independent positive token budgets including manual model overrides', () => {
  assert.equal(reviewLimits(paid, false, {}).tokens, 500000);
  assert.equal(reviewLimits(paid, true, {}).tokens, 150000);
  const env = {OCR_PAID_REVIEW_TOKENS: '750000', OCR_PAID_REREVIEW_TOKENS: '200000'};
  assert.equal(reviewLimits(paid, false, env).tokens, 750000);
  assert.equal(reviewLimits(paid, true, env).tokens, 200000);
  assert.equal(reviewLimits(local, false, env).tokens, 0);
  assert.equal(reviewLimits(paid, false, env).tools, 100);
  for (const value of ['0', '-1', 'Infinity', '1e6', '1.5', ' 10', '10\n', '2147483648']) {
    for (const cheap of [false, true]) {
      const key = cheap ? 'OCR_PAID_REREVIEW_TOKENS' : 'OCR_PAID_REVIEW_TOKENS';
      assert.throws(() => reviewLimits(paid, cheap, {[key]: value}), /positive integer/);
    }
  }
  assert.throws(() => reviewLimits('openrouter/auto', false, {}));
});

test('review clocks are adjustable and always reserve publication/cleanup time', () => {
  for (const model of [local, paid]) {
    const normal = reviewLimits(model, false, {OCR_REVIEW_MINUTES: '240'});
    assert.equal(normal.minutes, 240);
    assert.equal(normal.jobMinutes, 250);
    assert.equal(reviewLimits(model, true, {OCR_REREVIEW_MINUTES: '30'}).minutes, 30);
    for (const value of ['0', '-1', '331', 'NaN']) {
      assert.throws(() => reviewLimits(model, false, {OCR_REVIEW_MINUTES: value}));
      assert.throws(() => reviewLimits(model, true, {OCR_REREVIEW_MINUTES: value}));
    }
  }
});

test('local repair never contacts OpenRouter or requires credentials/budget', async () => {
  await requireRepairBudget(local, '', () => {throw new Error('unexpected paid API call');});
});

test('the exact free cloud variant needs a key but no spend allowance or paid fallback', async () => {
  const free = 'openrouter/qwen/qwen3.8-27b:free';
  const noFetch = () => {throw new Error('unexpected budget lookup');};
  await requireRepairBudget(free, 'test-placeholder', noFetch);
  await assert.rejects(requireRepairBudget(free, '', noFetch), /requires OPENROUTER_API_KEY/);
  assert.equal(reviewLimits(free, false, {}).tokens, 500000, 'hosted free route keeps its token budget');
  assert.equal(opencode(free).model, free);
  assert.equal(opencode(free).small_model, free);
  assert.deepEqual(opencode(free).enabled_providers, ['openrouter']);
  // Dropping :free must not bypass the dollar guard for the paid model.
  await assert.rejects(requireRepairBudget(free.replace(':free', ''), 'test-placeholder', async () => ({
    ok: true, json: async () => ({data: {limit: null}}),
  })), /requires an OpenRouter key/);
});

test('paid repair verifies the selected key cap without requesting inference or following redirects', async () => {
  let calls = 0;
  await requireRepairBudget(paid, 'test-placeholder', async (url, options) => {
    calls++;
    assert.equal(url, 'https://openrouter.ai/api/v1/key');
    assert.equal(options.headers.Authorization, 'Bearer test-placeholder');
    assert.equal(options.redirect, 'error');
    assert.ok(options.signal instanceof AbortSignal);
    return {ok: true, json: async () => ({data: {limit: 5, limit_remaining: 3, include_byok_in_limit: true}})};
  });
  assert.equal(calls, 1);
});

test('paid repair refuses absent, exhausted, malformed or BYOK-exempt spending limits', async () => {
  const valid = {limit: 5, limit_remaining: 3, include_byok_in_limit: true};
  for (const data of [undefined, {}, {...valid, limit: null}, {...valid, limit: '5'},
    {...valid, limit: Infinity}, {...valid, limit_remaining: null}, {...valid, limit_remaining: 0},
    {...valid, limit_remaining: -1}, {...valid, include_byok_in_limit: false},
    {...valid, include_byok_in_limit: undefined}]) {
    await assert.rejects(requireRepairBudget(paid, 'test-placeholder', async () => ({
      ok: true, json: async () => ({data}),
    })), /requires an OpenRouter key/);
  }
  await assert.rejects(requireRepairBudget(paid, '', () => {throw new Error('must not call');}), /requires OPENROUTER_API_KEY/);
});

test('budget lookup failures stop paid repair without leaking provider payloads or credentials', async () => {
  for (const fetcher of [
    async () => {throw new Error('secret-key-value');},
    async () => ({ok: false, json: async () => {throw new Error('must not read body');}}),
    async () => ({ok: true, json: async () => {throw new Error('private response');}}),
  ]) {
    await assert.rejects(requireRepairBudget(paid, 'secret-key-value', fetcher), {
      message: 'Cannot verify OpenRouter spending limit; no paid repair started',
    });
  }
});
