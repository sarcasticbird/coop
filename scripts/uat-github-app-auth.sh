#!/bin/bash
# Read-only live acceptance. Native exec avoids minting fresh credentials on entry.
set -euo pipefail
if [[ $# != 5 ]]; then echo 'usage: uat-github-app-auth.sh agent code-owner planning-owner private-repo expected-bot' >&2; exit 64; fi
python3 - "$@" <<'PY'
import datetime, json, os, re, subprocess, sys
agent, code, planning, private_repo, expected_bot = sys.argv[1:]
if not all(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,62}', v) for v in (agent, code, planning)):
    raise SystemExit('invalid agent or owner')
if not re.fullmatch(r'[A-Za-z0-9_.-]+', private_repo):
    raise SystemExit('invalid private repository')
cli = os.environ.get('COOP_CONTAINER_BIN', 'container')
name = 'coop-agent-' + agent
inspection = subprocess.run([cli, 'machine', 'inspect', name], capture_output=True, check=True)
rows = json.loads(inspection.stdout)
if len(rows) != 1 or rows[0]['id'] != name or rows[0]['status'] != 'running' or rows[0]['homeMount'] != 'none':
    raise SystemExit('selected agent is not running and unshared')
child = rows[0]['containerId']
base = [cli, 'exec', '--user', 'root', '--workdir', '/workspace', child, '/usr/local/libexec/coop-env', '/usr/local/libexec/coop-github']
def invoke(args, fail=False):
    result = subprocess.run(base + args, capture_output=True, timeout=90)
    if fail:
        if result.returncode == 0 or result.stdout:
            raise SystemExit('unsafe operation was not refused')
        return None
    if result.returncode != 0:
        raise SystemExit('live App acceptance operation failed; inspect safe helper status')
    return result.stdout
def api(owner, args):
    return json.loads(invoke(['gh', '--owner', owner, '--', 'api'] + args))
status = json.loads(invoke(['status', '--json']))
if status['bot_login'] != expected_bot or set(status['installations']) != {code, planning}:
    raise SystemExit('credential identity or owner set mismatch')
for owner in (code, planning):
    viewer = api(owner, ['graphql', '-f', 'query=query { viewer { login } }'])
    if viewer.get('errors') or viewer['data']['viewer']['login'] != expected_bot:
        raise SystemExit('authenticated bot identity mismatch')
pages = api(code, ['--paginate', '--slurp', '/installation/repositories?per_page=100'])
repos = [repo for page in pages for repo in page['repositories']]
if any(repo['owner']['login'].lower() != code.lower() for repo in repos):
    raise SystemExit('repository discovery escaped configured owner')
private = api(code, ['/repos/' + code + '/' + private_repo])
if not private['private']:
    raise SystemExit('private access fixture is not private')
invoke(['env', '--', 'git', 'ls-remote', 'https://github.com/' + code + '/' + private_repo + '.git'])
projects = api(planning, ['graphql', '-f', 'query=query($owner:String!) { organization(login:$owner) { projectsV2(first:1) { totalCount } } }', '-f', 'owner=' + planning])
if projects.get('errors'):
    raise SystemExit('planning owner access denied')
invoke(['gh', '--owner', code, '--', 'auth', 'status', '--show-token'], fail=True)
invoke(['gh', '--owner', 'unauthorized-owner', '--', 'api', '/installation/repositories'], fail=True)
print(json.dumps({'agent': agent, 'bot_login': expected_bot, 'repository_count': len(repos), 'private_count': sum(repo['private'] for repo in repos), 'private_git_read': True, 'planning_read': True, 'secret_dump_refused': True, 'unknown_owner_refused': True, 'credentials': status, 'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}, indent=2))
PY
