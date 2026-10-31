"""Publish fixed source snapshots without changing their branches."""
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request

REPO = 'system-thoughts/perf-tests'
REGISTRY = 'ghcr.io/' + REPO
SOURCE = Path('source')
DIST = Path('dist')
BRANCH = os.environ.get('SOURCE_BRANCH', '')
SHA = os.environ.get('SOURCE_SHA', '')
VERSION = BRANCH + '-' + SHA[:12]
IMAGES = {'Tools': 'proxy-bench-tools', 'Worker': 'proxy-bench'}
FILE_SERVER = 'docker.io/dragonflyoss/file-server@sha256:2ba1179ee4c2634284311cba657b3981482e2061109a8bc1e53597b70a1199d9'
ACCEPT = ', '.join(['application/vnd.oci.image.index.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
    'application/vnd.oci.image.manifest.v1+json',
    'application/vnd.docker.distribution.manifest.v2+json'])
TOKENS = {}


def api(path):
    req = urllib.request.Request('https://api.github.com/repos/' + REPO + path,
        headers={'Authorization': 'Bearer ' + os.environ['GH_TOKEN'],
                 'Accept': 'application/vnd.github+json', 'User-Agent': 'perf-tests-delivery'})
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def registry(image, kind, ref):
    repo = REPO + '/' + image
    if repo not in TOKENS:
        query = urllib.parse.urlencode({'service': 'ghcr.io', 'scope': f'repository:{repo}:pull'})
        with urllib.request.urlopen('https://ghcr.io/token?' + query, timeout=30) as response:
            TOKENS[repo] = json.load(response)['token']
    req = urllib.request.Request(f'https://ghcr.io/v2/{repo}/{kind}/{ref}',
        headers={'Authorization': 'Bearer ' + TOKENS[repo], 'Accept': ACCEPT})
    for attempt in range(3):
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                raw = response.read()
                digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
                if ref.startswith('sha256:'):
                    assert digest == ref, (image, ref)
                if kind == 'manifests':
                    assert response.headers['Docker-Content-Digest'] == digest
                return json.loads(raw), digest
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return None
            raise
        except urllib.error.URLError:
            if attempt == 2:
                raise
            time.sleep(attempt + 1)


def inspect(image, aliases=False):
    result = registry(image, 'manifests', VERSION)
    if result is None:
        return None
    index, digest = result
    manifests = index.get('manifests', [])
    assert len(manifests) == 2, (image, 'expected exactly two runnable platforms')
    platforms = {}
    for entry in manifests:
        platform = entry['platform']
        arch = platform['architecture']
        assert platform['os'] == 'linux' and arch in ('amd64', 'arm64')
        assert arch not in platforms
        assert entry.get('annotations', {}).get('vnd.docker.reference.type') != 'attestation-manifest'
        manifest, _ = registry(image, 'manifests', entry['digest'])
        config, _ = registry(image, 'blobs', manifest['config']['digest'])
        assert config['architecture'] == arch
        labels = config['config']['Labels']
        assert labels['org.opencontainers.image.revision'] == SHA
        assert labels['org.opencontainers.image.source'] == 'https://github.com/' + REPO
        alias = registry(image, 'manifests', VERSION + '-' + arch)
        if alias is not None:
            assert alias[1] == entry['digest'], (image, arch, 'existing platform tag differs')
        elif aliases:
            raise RuntimeError(f'missing platform tag: {image}:{VERSION}-{arch}')
        platforms[arch] = entry['digest']
    assert set(platforms) == {'amd64', 'arm64'}
    return {'digest': digest, 'platforms': platforms}


def gh(*args):
    subprocess.run(['gh', *args, '--repo', REPO], check=True)


def output(key, value):
    with open(os.environ['GITHUB_OUTPUT'], 'a') as stream:
        text = str(value).lower() if isinstance(value, bool) else str(value)
        stream.write(f'{key}={text}\n')


def read_fields(path):
    return dict(line.split(': ', 1) for line in path.read_text().splitlines() if ': ' in line)


def download_info(release):
    if release and any(a['name'] == 'build-info.txt' for a in release['assets']):
        gh('release', 'download', VERSION, '--pattern', 'build-info.txt', '--dir', str(DIST), '--clobber')
        return read_fields(DIST / 'build-info.txt')
    return None


def validate_info(fields, images):
    assert fields['Version'] == VERSION and fields['Branch'] == BRANCH
    assert fields['Source'] == 'https://github.com/' + REPO + '/commit/' + SHA
    assert fields['Engine'] == os.environ['LOAD_ENGINE']
    for role, image in IMAGES.items():
        assert images[role] is not None
        assert fields[role] == f'{REGISTRY}/{image}@{images[role]["digest"]}'
        for arch in ('amd64', 'arm64'):
            assert fields[role + '-' + arch] == f'{REGISTRY}/{image}:{VERSION}-{arch}@{images[role]["platforms"][arch]}'


def resolve():
    sources = [
        ('fixed_pool_refactor', 'k6', 'tools/proxy-bench/Dockerfile.tools'),
        ('nydus_hot_dev', 'vegeta', 'tools/proxy-bench/fixed-pool/Dockerfile.tools'),
        ('fixed_pool_refactor_vegeta', 'vegeta', 'tools/proxy-bench/Dockerfile.tools'),
    ]
    include = [{'branch': branch, 'sha': api('/git/ref/heads/' + branch)['object']['sha'],
                'engine': engine, 'tools_file': tools_file} for branch, engine, tools_file in sources]
    output('matrix', json.dumps({'include': include}))
    print(json.dumps(include, indent=2))


def preflight():
    assert os.environ['GITHUB_REPOSITORY'] == REPO
    assert api('/git/ref/heads/' + BRANCH)['object']['sha'] == SHA
    assert subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=SOURCE, text=True).strip() == SHA
    DIST.mkdir(exist_ok=True)
    release = api('/releases/tags/' + VERSION)
    if release:
        assert release['target_commitish'] == SHA
    images = {role: inspect(image) for role, image in IMAGES.items()}
    fields = download_info(release)
    if fields:
        validate_info(fields, images)
    complete = bool(release and not release['draft'])
    if complete:
        assert fields is not None
        expected_assets = {'build-info.txt', 'SHA256SUMS', f'fixed-pool-deploy-{VERSION}.tar.gz'}
        assert {a['name'] for a in release['assets']} == expected_assets
        for role, image in IMAGES.items():
            inspect(image, aliases=True)
        assert api('/git/ref/tags/' + VERSION)['object']['sha'] == SHA
        gh('release', 'download', VERSION, '--dir', str(DIST), '--clobber')
        subprocess.run(['sha256sum', '--check', 'SHA256SUMS'], cwd=DIST, check=True)
        for asset in release['assets']:
            assert asset['digest'] == 'sha256:' + hashlib.sha256((DIST / asset['name']).read_bytes()).hexdigest()
        print('Complete existing delivery verified; reusing without rebuilding or replacing it')
    output('version', VERSION)
    output('complete', complete)
    for role in IMAGES:
        output(role.lower() + '_exists', images[role] is not None)


def aliases():
    for role, image in IMAGES.items():
        info = inspect(image)
        assert info is not None
        for arch, digest in info['platforms'].items():
            if registry(image, 'manifests', VERSION + '-' + arch) is None:
                subprocess.run(['docker', 'buildx', 'imagetools', 'create', '--prefer-index=false',
                    '--tag', f'{REGISTRY}/{image}:{VERSION}-{arch}', f'{REGISTRY}/{image}@{digest}'], check=True)
        inspect(image, aliases=True)


def package(images):
    existing = DIST / 'build-info.txt'
    if existing.exists():
        validate_info(read_fields(existing), images)
    else:
        lines = [f'Version: {VERSION}', f'Branch: {BRANCH}',
                 f'Source: https://github.com/{REPO}/commit/{SHA}',
                 f'Build: https://github.com/{REPO}/actions/runs/{os.environ["GITHUB_RUN_ID"]}/attempts/{os.environ["GITHUB_RUN_ATTEMPT"]}',
                 'Platforms: linux/amd64,linux/arm64', f'Engine: {os.environ["LOAD_ENGINE"]}',
                 f'FileServer: {FILE_SERVER}']
        for role, image in IMAGES.items():
            lines.extend([f'{role}: {REGISTRY}/{image}@{images[role]["digest"]}',
                          f'{role}-Tag: {REGISTRY}/{image}:{VERSION}'])
            for arch, digest in images[role]['platforms'].items():
                lines.append(f'{role}-{arch}: {REGISTRY}/{image}:{VERSION}-{arch}@{digest}')
        existing.write_text('\n'.join(lines) + '\n')
    bundle = DIST / 'fixed-pool-deploy'
    if bundle.exists():
        shutil.rmtree(bundle)
    bundle.mkdir()
    for folder in ('deploy', 'examples'):
        shutil.copytree(SOURCE / 'tools/proxy-bench/fixed-pool' / folder, bundle / folder,
                        ignore=shutil.ignore_patterns('*.md'))
    shutil.copyfile(existing, bundle / existing.name)
    archive = DIST / f'fixed-pool-deploy-{VERSION}.tar.gz'
    epoch = int(subprocess.check_output(['git', 'show', '-s', '--format=%ct', SHA], cwd=SOURCE, text=True))
    with archive.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for path in [bundle, *sorted(bundle.rglob('*'))]:
                info = tar.gettarinfo(str(path), arcname=str(path.relative_to(DIST)))
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mtime = epoch
                if path.is_file():
                    with path.open('rb') as body:
                        tar.addfile(info, body)
                else:
                    tar.addfile(info)
    assets = [existing, archive]
    (DIST / 'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n' for p in assets))
    return [existing, archive, DIST / 'SHA256SUMS']


def publish():
    images = {role: inspect(image, aliases=True) for role, image in IMAGES.items()}
    assets = package(images)
    release = api('/releases/tags/' + VERSION)
    assert release is None or release['draft']
    note = DIST / 'release.md'
    note.write_text(f'''Development delivery for **{BRANCH}**, fixed-pool engine **{os.environ['LOAD_ENGINE']}**.

Source: https://github.com/{REPO}/commit/{SHA}

| Image | Purpose | Default multi-platform tag |
|---|---|---|
| proxy-bench-tools | Dataset, plan and Seed preparation | `{REGISTRY}/proxy-bench-tools:{VERSION}` |
| proxy-bench | Benchmark worker | `{REGISTRY}/proxy-bench:{VERSION}` |

The default tag selects linux/amd64 or linux/arm64. Append `-amd64` or `-arm64` to select a specific architecture.
Use `build-info.txt` for exact image digests and the fixed external file-server dependency.
Verify the attached files with `sha256sum -c SHA256SUMS`, then extract `fixed-pool-deploy-{VERSION}.tar.gz`.
The archive contains deployment material from exactly this source commit. Keep its tools, worker and deployment files together.
Use this branch's existing preparation commands and fixed-pool Job entrypoint; branch layouts are intentionally preserved.

[Source usage instructions](https://github.com/{REPO}/blob/{SHA}/tools/proxy-bench/README.md)

This development publication is not ARM-native, load-generator capacity or scale validation.
''')
    if release is None:
        gh('release', 'create', VERSION, '--target', SHA, '--title', VERSION,
           '--draft', '--prerelease', '--notes-file', str(note))
        release = api('/releases/tags/' + VERSION)
    for path in assets:
        previous = next((a for a in release['assets'] if a['name'] == path.name), None)
        if previous:
            assert previous['digest'] == 'sha256:' + hashlib.sha256(path.read_bytes()).hexdigest(), path.name
        else:
            gh('release', 'upload', VERSION, str(path))
    gh('release', 'edit', VERSION, '--draft=false', '--prerelease', '--latest=false')
    final = api('/releases/tags/' + VERSION)
    assert not final['draft'] and final['prerelease'] and final['target_commitish'] == SHA
    assert api('/git/ref/tags/' + VERSION)['object']['sha'] == SHA
    assert api('/git/ref/heads/' + BRANCH)['object']['sha'] == SHA
    summary = Path(os.environ['GITHUB_STEP_SUMMARY'])
    with summary.open('a') as stream:
        stream.write(f'## {VERSION}\n\n[Complete delivery]({final["html_url"]})\n\n```text\n' + (DIST / 'build-info.txt').read_text() + '```\n')
    (DIST / 'verification.json').write_text(json.dumps({'version': VERSION, 'source': SHA, 'images': images,
        'release': final['html_url'], 'result': 'passed'}, indent=2) + '\n')


if __name__ == '__main__':
    {'resolve': resolve, 'preflight': preflight, 'aliases': aliases, 'publish': publish}[sys.argv[1]]()
