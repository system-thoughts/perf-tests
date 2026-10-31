"""One-time, explicitly authorized reset of this repository's GHCR versions."""
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request

REPO = 'system-thoughts/perf-tests'
OWNER = 'system-thoughts'
EXPECTED = {
    'fixed_pool_refactor': '59c90682dad6ba5340e48a802f443e96f1ba6b55',
    'nydus_hot_dev': 'a03c9b9b06bc91d6c8f9f2d8e7a97d1b2c2b4611',
    'fixed_pool_refactor_vegeta': 'df2c8e6d7219729d502f5b09d74f0da2fead63b1',
}
PACKAGES = {'perf-tests/proxy-bench', 'perf-tests/proxy-bench-tools'}


def api(path, method='GET'):
    request = urllib.request.Request('https://api.github.com' + path, method=method,
        headers={'Authorization': 'Bearer ' + os.environ['GH_TOKEN'],
                 'Accept': 'application/vnd.github+json',
                 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'perf-tests-delivery'})
    try:
        with urllib.request.urlopen(request, timeout=45) as response:
            raw = response.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        error.detail = error.read().decode()
        print(method, path, error.code, error.detail[:1000], flush=True)
        raise


def pages(path):
    values = []
    for page in range(1, 101):
        result = api(path + ('&' if '?' in path else '?') + f'per_page=100&page={page}')
        values.extend(result)
        if len(result) < 100:
            return values
    raise RuntimeError('unexpected package pagination size')


assert os.environ['GITHUB_REPOSITORY'] == REPO
for branch, sha in EXPECTED.items():
    assert api(f'/repos/{REPO}/git/ref/heads/{branch}')['object']['sha'] == sha, branch
selected = [api(f'/users/{OWNER}/packages/container/' + urllib.parse.quote(name, safe='')) for name in sorted(PACKAGES)]
assert all((p.get('repository') or {}).get('full_name') == REPO for p in selected)
assert {p['name'] for p in selected} == PACKAGES, [p['name'] for p in selected]
report = {'repository': REPO, 'source_heads': EXPECTED, 'packages': [], 'deleted': [], 'protected': []}
out = Path('reset-evidence')
out.mkdir(exist_ok=True)
for package in selected:
    assert package['visibility'] == 'public' and package['package_type'] == 'container'
    path = f'/users/{OWNER}/packages/container/' + urllib.parse.quote(package['name'], safe='')
    versions = pages(path + '/versions?state=active')
    report['packages'].append({'metadata': package, 'versions': versions, 'path': path})
(out / 'before.json').write_text(json.dumps(report, indent=2) + '\n')
for package in report['packages']:
    for version in package['versions']:
        version_id = int(version['id'])
        try:
            api(package['path'] + f'/versions/{version_id}', 'DELETE')
        except urllib.error.HTTPError as error:
            if error.code == 400 and '5000 downloads' in error.detail:
                report['protected'].append({'package': package['metadata']['name'], 'id': version_id,
                    'digest': version['name'], 'tags': version['metadata']['container']['tags'], 'reason': error.detail})
                continue
            raise
        report['deleted'].append({'package': package['metadata']['name'], 'id': version_id,
                                  'digest': version['name'], 'tags': version['metadata']['container']['tags']})
        (out / 'progress.json').write_text(json.dumps(report, indent=2) + '\n')
        time.sleep(1)  # Stay below GitHub's content-generating request limit.
    remaining = pages(package['path'] + '/versions?state=active')
    protected = {v['id'] for v in report['protected'] if v['package'] == package['metadata']['name']}
    assert {v['id'] for v in remaining} == protected, package['metadata']['name']
report['result'] = 'protected_remaining' if report['protected'] else 'empty'
(out / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
print(f"Removed {len(report['deleted'])} versions; {len(report['protected'])} protected versions remain: {report['result']}")
