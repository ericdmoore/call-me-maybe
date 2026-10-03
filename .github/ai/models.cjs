// Exact provider/model identifiers; no aliases that can choose a paid fallback.
function route(model) {
  if (model === 'ollama/bullmoose-ocr:20b') return {
    provider: 'ollama', model: 'bullmoose-ocr:20b', url: 'http://127.0.0.1:11434/v1',
  };
  if (/^openrouter\/[A-Za-z0-9_.:-]+\/[A-Za-z0-9_.:-]+$/.test(model)) return {
    provider: 'openrouter', model: model.slice('openrouter/'.length), url: 'https://openrouter.ai/api/v1',
  };
  throw new Error('Use ollama/bullmoose-ocr:20b or openrouter/vendor/exact-model-id (no trailing slash)');
}
function opencode(model) {
  const selected = route(model);
  return {
    $schema: 'https://opencode.ai/config.json', model, small_model: model,
    enabled_providers: [selected.provider], autoupdate: false, share: 'disabled',
    permission: {external_directory: 'deny', question: 'deny'},
    provider: selected.provider === 'ollama' ? {ollama: {
      npm: '@ai-sdk/openai-compatible', name: 'Alpaca local',
      options: {baseURL: selected.url, apiKey: 'ollama', timeout: 1200000},
      models: {'bullmoose-ocr:20b': {name: 'Local GPT-OSS 20B', limit: {context: 65536, output: 16384}}},
    }} : {},
  };
}
function credential(model, key) {
  if (route(model).provider === 'ollama') return 'ollama';
  if (!key) throw new Error('Explicit OpenRouter selection requires OPENROUTER_API_KEY; no fallback');
  return key;
}
module.exports = {route, opencode, credential};
