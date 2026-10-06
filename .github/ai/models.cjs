// Exact provider/model identifiers; no aliases that can choose a paid fallback.
//
// The local model is a LiteLLM alias on Alpaca, not an Ollama name. LiteLLM is
// where this box sets a local model's parameters (the 64K context lives in its
// config beside every other alias), where usage is counted, and where the
// runner's key is scoped to this one alias and nothing paid. Ollama keeps one
// model name underneath. The older Ollama ids are still accepted and mapped
// here, so the repository variables and old /oc comments keep working.
const fs = require('node:fs');
const path = require('node:path');
const LOCAL_ALIAS = '@local/openai/gpt-oss:20b-64k';
const LOCAL_MODEL = `litellm/${LOCAL_ALIAS}`;
const LOCAL_URL = 'http://alpaca.local:4000/v1';
const LEGACY_LOCAL = new Set(['ollama/gpt-oss:20b', 'ollama/bullmoose-ocr:20b']);
const FREE_MODEL = 'openrouter/qwen/qwen3.8-27b:free';
// The variable OpenCode substitutes the key from at run time; the workflow
// exports it from the host file for the one step that needs it.
const LOCAL_KEY_ENV = 'LITELLM_REVIEW_KEY';
function normalize(model) {
  return LEGACY_LOCAL.has(model) ? LOCAL_MODEL : model;
}
function route(model) {
  if (normalize(model) === LOCAL_MODEL) return {provider: 'litellm', model: LOCAL_ALIAS, url: LOCAL_URL};
  // Exact vendor/model ids only. OpenRouter's own "openrouter/auto" and
  // "openrouter/free" pick a model per request, and ":online", ":nitro" and
  // similar variants add per-request cost; ":free" is a price, not a router,
  // so it is the one suffix allowed through.
  if (/^openrouter\/(?!openrouter\/)[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+(?::free)?$/.test(model)) return {
    provider: 'openrouter', model: model.slice('openrouter/'.length), url: 'https://openrouter.ai/api/v1',
  };
  throw new Error(`Use ${LOCAL_MODEL} or openrouter/vendor/exact-model-id, optionally :free (no routers, other variants or trailing slash)`);
}
function isLocal(model) {
  return route(model).provider === 'litellm';
}
function opencode(model) {
  model = normalize(model);
  const selected = route(model);
  return {
    $schema: 'https://opencode.ai/config.json', model, small_model: model,
    enabled_providers: [selected.provider], autoupdate: false, share: 'disabled',
    permission: {external_directory: 'deny', question: 'deny'},
    provider: selected.provider === 'litellm' ? {litellm: {
      npm: '@ai-sdk/openai-compatible', name: 'Alpaca LiteLLM',
      // Substituted by OpenCode from the job's environment when it runs, so
      // the written config never holds the key.
      options: {baseURL: selected.url, apiKey: `{env:${LOCAL_KEY_ENV}}`, timeout: 1200000},
      models: {[selected.model]: {id: selected.model, name: 'Local GPT-OSS 20B, 64K context', limit: {context: 65536, output: 16384}}},
    }} : {},
  };
}
// The credential for a route, and nothing falls back. Local: the LiteLLM key
// the host keeps for this runner, in the file LITELLM_KEY_FILE names, outside
// any checkout; it is scoped server-side to the one local alias, so even a
// misrouted request cannot reach anything paid. Cloud: the explicit OpenRouter
// secret.
function credential(model, key, keyFile = process.env.LITELLM_KEY_FILE) {
  if (isLocal(model)) {
    if (!keyFile || !path.isAbsolute(keyFile)) throw new Error('LITELLM_KEY_FILE must name the host key file by absolute path; no fallback');
    let value;
    try {
      value = fs.readFileSync(keyFile, 'utf8').trim();
    } catch {
      throw new Error('The LiteLLM key file is unreadable; no fallback');
    }
    if (!value) throw new Error('The LiteLLM key file is empty; no fallback');
    return value;
  }
  if (!key) throw new Error('Explicit OpenRouter selection requires OPENROUTER_API_KEY; no fallback');
  return key;
}
module.exports = {LOCAL_ALIAS, LOCAL_MODEL, LOCAL_KEY_ENV, FREE_MODEL, normalize, route, isLocal, opencode, credential};
