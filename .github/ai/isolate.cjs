// A child process gets its own home/config/credentials. Never changes the login
// shell's HOME or reads/copies Alpaca's OCR, OpenCode or gh authentication.
const fs = require('node:fs');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
function isolatedEnv(root, extra = {}) {
  const jobHome = path.join(root, 'home');
  fs.mkdirSync(jobHome, {recursive: true, mode: 0o700});
  return {...process.env, HOME: jobHome, XDG_CONFIG_HOME: path.join(jobHome, '.config'),
    XDG_DATA_HOME: path.join(jobHome, '.local/share'), XDG_CACHE_HOME: path.join(jobHome, '.cache'),
    XDG_STATE_HOME: path.join(jobHome, '.local/state'), GH_CONFIG_DIR: path.join(jobHome, '.config/gh'),
    GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
    OCR_ENABLE_TELEMETRY: 'false', OCR_NO_UPDATE: '1', OPENCODE_DISABLE_PROJECT_CONFIG: 'true',
    OPENCODE_DISABLE_AUTOUPDATE: 'true', OPENCODE_CONFIG: '', OPENCODE_CONFIG_DIR: '',
    OTEL_EXPORTER_OTLP_ENDPOINT: '', OTEL_EXPORTER_OTLP_HEADERS: '', ...extra};
}
if (require.main === module) {
  const [root, command, ...args] = process.argv.slice(2);
  const result = spawnSync(command, args, {env: isolatedEnv(root), stdio: 'inherit'});
  if (result.error) console.error(result.error.message);
  process.exit(result.status ?? 1);
}
module.exports = {isolatedEnv};
