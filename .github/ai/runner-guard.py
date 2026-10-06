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
    workflow = env.get('GITHUB_WORKFLOW_REF')
    previews = {name: f'{REPOSITORY}/.github/workflows/{name}@refs/heads/main'
                for name in ('site-preview.yml', 'site-preview-cleanup.yml')}
    if workflow in previews.values():
        cleanup = workflow == previews['site-preview-cleanup.yml']
        pr = event.get('pull_request', {})
        return (env.get('GITHUB_EVENT_NAME') == 'pull_request_target'
                and event.get('action') in ({'closed'} if cleanup else {'opened', 'synchronize', 'reopened'})
                and pr.get('head', {}).get('repo', {}).get('full_name') == REPOSITORY
                # A preview exists only for PRs into main, but one retargeted
                # before it closed still has a preview to remove.
                and (cleanup or pr.get('base', {}).get('ref') == 'main'))
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


def decide(env, event_path):
    """Fail closed: any surprise in the payload or environment is a refusal.

    GitHub sends ``"repo": null`` for a deleted fork and ``pull_request`` can be
    absent; ``allowed`` then raises. An error that escaped here would exit
    without stopping the Worker, and ``if: always()`` steps would run anyway.
    """
    try:
        with open(event_path) as source:
            return allowed(env, json.load(source)) is True
    except Exception:  # noqa: BLE001 — the guard is the boundary
        return False


if __name__ == '__main__':
    if not decide(os.environ, os.environ.get('GITHUB_EVENT_PATH', '')):
        print('Alpaca guard rejected this event/workflow; stopping its Worker before job steps.', flush=True)
        stop_worker()
        raise SystemExit(1)
