const {route, credential, FREE_MODEL} = require('./models.cjs');

function positiveInteger(value, fallback, name, max) {
  if (value === undefined || value === '') return fallback;
  if (!/^[1-9]\d*$/.test(String(value)) || !Number.isSafeInteger(Number(value)) || Number(value) > max) {
    throw new Error(`${name} must be a positive integer no greater than ${max}`);
  }
  return Number(value);
}

function reviewLimits(model, cheap, env = process.env) {
  const local = route(model).provider === 'ollama';
  const timeKey = cheap ? 'OCR_REREVIEW_MINUTES' : 'OCR_REVIEW_MINUTES';
  const budgetKey = cheap ? 'OCR_PAID_REREVIEW_TOKENS' : 'OCR_PAID_REVIEW_TOKENS';
  const minutes = positiveInteger(env[timeKey], cheap ? 20 : 80, timeKey, 330);
  return {
    minutes, jobMinutes: minutes + 10, // Leave time to publish failure/coverage and clean up.
    tokens: local ? 0 : positiveInteger(env[budgetKey], cheap ? 150000 : 500000, budgetKey, 2147483647),
    // OCR 1.12.11 treats --max-tools=0 as its 100-turn default, not unlimited.
    // This positive value is unreachable before the clock deadline; no fork needed.
    tools: local ? 2147483647 : 100,
    taskMinutes: local ? 0 : 45,
  };
}

async function requireRepairBudget(model, key, fetcher = fetch) {
  if (route(model).provider === 'ollama') return; // No key or network dependency locally.
  credential(model, key);
  // This exact :free variant cannot route to the paid variant. Keep cloud
  // token/time limits and authentication, but do not require a spending allowance.
  if (model === FREE_MODEL) return;
  let data;
  try {
    const response = await fetcher('https://openrouter.ai/api/v1/key', {
      headers: {Authorization: `Bearer ${key}`}, redirect: 'error', signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error('Key lookup failed');
    ({data} = await response.json());
  } catch {
    // Never expose the response, key metadata, or an exception that might carry credentials.
    throw new Error('Cannot verify OpenRouter spending limit; no paid repair started');
  }
  if (!Number.isFinite(data?.limit) || data.limit <= 0 ||
      !Number.isFinite(data?.limit_remaining) || data.limit_remaining <= 0 ||
      data.include_byok_in_limit !== true) {
    throw new Error('Paid repair requires an OpenRouter key with a positive spending limit, remaining allowance, and Include BYOK in limit enabled. Configure the repository key at https://openrouter.ai/settings/keys');
  }
}

module.exports = {reviewLimits, requireRepairBudget};
