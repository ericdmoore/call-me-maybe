// Exact provider/model identifiers; no aliases that can choose a paid fallback.
const LOCAL_MODEL = 'ollama/gpt-oss:20b';
const FREE_MODEL = 'openrouter/qwen/qwen3.8-27b:free';
function normalize(model) {
  // Compatibility with the original repository variable and old /oc requests.
  return model === 'ollama/bullmoose-ocr:20b' ? LOCAL_MODEL : model;
}
function route(model) {
  if (normalize(model) === LOCAL_MODEL) return {
    // Reuse the existing GPT-OSS weights + 65K context preset. The stock model
    // has no num_ctx override; changing it would affect other Alpaca consumers.
    provider: 'ollama', model: 'bullmoose-ocr:20b', url: 'http://127.0.0.1:11434/v1',
  };
  if (/^openrouter\/[A-Za-z0-9_.:-]+\/[A-Za-z0-9_.:-]+$/.test(model)) return {
    provider: 'openrouter', model: model.slice('openrouter/'.length), url: 'https://openrouter.ai/api/v1',
  };
  throw new Error('Use ollama/gpt-oss:20b or openrouter/vendor/exact-model-id (no trailing slash)');
}
function opencode(model) {
  model = normalize(model);
  const selected = route(model);
  return {
    $schema: 'https://opencode.ai/config.json', model, small_model: model,
    enabled_providers: [selected.provider], autoupdate: false, share: 'disabled',
    permission: {external_directory: 'deny', question: 'deny'},
    provider: selected.provider === 'ollama' ? {ollama: {
      npm: '@ai-sdk/openai-compatible', name: 'Alpaca local',
      options: {baseURL: selected.url, apiKey: 'ollama', timeout: 1200000},
      models: {'gpt-oss:20b': {id: selected.model, name: 'Local GPT-OSS 20B', limit: {context: 65536, output: 16384}}},
    }} : {},
  };
}
function credential(model, key) {
  if (route(model).provider === 'ollama') return 'ollama';
  if (!key) throw new Error('Explicit OpenRouter selection requires OPENROUTER_API_KEY; no fallback');
  return key;
}
module.exports = {LOCAL_MODEL, FREE_MODEL, normalize, route, opencode, credential};
