module.exports = function coverage(result) {
  const m = result.manifest;
  if (m?.schema_version !== 'ocr.run-manifest/v1' || !m.coverage) {
    return '⚠️ Coverage unknown: missing or unsupported manifest. Advisory only; not CI or approval.';
  }
  const c = m.coverage;
  const count = key => Array.isArray(c[key]) ? c[key].length : 0;
  const incomplete = count('failed') || count('waived') || m.terminal_state !== 'complete';
  const lines = [`${incomplete ? '⚠️ Incomplete' : 'Selected-file'} coverage at ${m.input.resolved_head}: ` +
    `${count('selected')} selected, ${count('completed')} completed, ${count('reused')} reused, ` +
    `${count('failed')} failed, ${count('waived')} waived. State: ${m.terminal_state}.`,
    'Excluded files and excerpted requirements are not complete coverage. Advisory only; not CI or approval.'];
  for (const key of ['failed', 'waived']) {
    for (const item of (c[key] || []).slice(0, 30)) lines.push(`- ${key}: ${item.path} (${item.classification || item.reason || 'unspecified'})`);
    if (count(key) > 30) lines.push(`- More ${key} items: see JSON artifact.`);
  }
  lines.push(`Warnings: ${(result.warnings || []).length}. See JSON artifact and live progress logs.`);
  return lines.join('\n');
};
