#!/usr/bin/env python3
"""Host-installed guard: a public PR must not request this personal runner directly."""
import json
import os
from pathlib import Path
import signal
import subprocess

REPOSITORY = 'ericdmoore/call-me-maybe'
WORKFLOWS = {'ocr-review.yml', 'opencode-repair.yml'}


def allowed(env, event):
    if env.get('GITHUB_REPOSITORY') != REPOSITORY:
        return False
    if env.get('GITHUB_WORKFLOW_REF') == f'{REPOSITORY}/.github/workflows/site-preview.yml@refs/heads/main':
        pr = event.get('pull_request', {})
        return (env.get('GITHUB_EVENT_NAME') == 'pull_request_target'
                and event.get('action') in {'opened', 'synchronize', 'reopened', 'closed'}
                and pr.get('head', {}).get('repo', {}).get('full_name') == REPOSITORY
                and pr.get('base', {}).get('ref') == 'main')
    refs = {f'{REPOSITORY}/.github/workflows/{name}@refs/heads/main' for name in WORKFLOWS}
    if env.get('GITHUB_WORKFLOW_REF') not in refs:
        return False
    kind = env.get('GITHUB_EVENT_NAME')
    if kind == 'pull_request_target':
        pr = event.get('pull_request', {})
        return (pr.get('head', {}).get('repo', {}).get('full_name') == REPOSITORY
                and not pr.get('draft', True) and pr.get('state') == 'open')
    if kind == 'issue_comment':
        return bool(event.get('issue', {}).get('pull_request'))
    return kind == 'workflow_dispatch'


def stop_worker():
    # Exit 1 alone is not a host boundary: a malicious workflow could use
    # `if: always()`. Terminate only this dedicated runner's ancestor Worker.
    pid = os.getppid()
    for _ in range(8):
        row = subprocess.check_output(['/bin/ps', '-p', str(pid), '-o', 'ppid=', '-o', 'comm='], text=True).strip()
        parent, command = row.split(None, 1)
        if Path(command).name == 'Runner.Worker':
            os.kill(pid, signal.SIGKILL)
            return
        pid = int(parent)
        if pid <= 1:
            break
    raise RuntimeError('No runner Worker ancestor; refusing outside runner context')


if __name__ == '__main__':
    try:
        with open(os.environ['GITHUB_EVENT_PATH']) as source:
            accepted = allowed(os.environ, json.load(source))
    except (KeyError, OSError, ValueError):
        accepted = False
    if not accepted:
        print('Alpaca guard rejected this event/workflow; stopping its Worker before job steps.', flush=True)
        stop_worker()
        raise SystemExit(1)
