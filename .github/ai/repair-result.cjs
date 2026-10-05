// A zero CLI exit is insufficient: provider errors and an empty agent stop can
// otherwise look like a successful assessment with no repairs needed.
module.exports = function repairResult(text) {
  const events = text.trim().split('\n').filter(Boolean).map(JSON.parse);
  if (!events.some(e => e.type === 'step_finish' && e.part?.reason === 'stop') ||
      !events.some(e => e.type === 'text' && e.part?.text?.trim()) ||
      events.some(e => e.type === 'error')) {
    throw new Error('OpenCode did not produce a completed assessment; no repair push');
  }
};
