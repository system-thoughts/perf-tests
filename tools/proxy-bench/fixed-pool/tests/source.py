#!/usr/bin/env python3
"""Maintainer regression for the real Kubernetes source bundle, not a user preflight.

Requires an existing test namespace, source node, StorageClass and immutable
tools/nginx images. Creates only uniquely named test resources. Uses a local
port-forward to test HTTP paths; it does not prove Seed-to-origin reachability.
"""
import argparse
from contextlib import contextmanager
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import time
import uuid


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


class Regression:
    def __init__(self, args):
        self.args = args
        self.root = Path(args.work_dir).resolve()
        self.root.mkdir(parents=True, exist_ok=False)
        self.source = Path(__file__).resolve().parents[1] / "deploy/source"
        self.name = "fp-source-" + uuid.uuid4().hex[:10]
        self.created, self.cases, self.sequence = [], [], 0

    def run(self, command, timeout=60, data=None, check=True):
        self.sequence += 1
        prefix = self.root / f"command-{self.sequence:03}"
        write(prefix.with_suffix(".argv.json"), command)
        result = subprocess.run(command, input=data, capture_output=True, timeout=timeout)
        prefix.with_suffix(".stdout").write_bytes(result.stdout)
        prefix.with_suffix(".stderr").write_bytes(result.stderr)
        prefix.with_suffix(".exit").write_text(str(result.returncode) + "\n")
        if check and result.returncode:
            raise RuntimeError(f"command failed; inspect {prefix}.stderr")
        return result

    def kube(self, *command, **kwargs):
        return self.run(["kubectl", "-n", self.args.namespace, *command], **kwargs)

    def get(self, kind, name):
        return json.loads(self.kube("get", kind, name, "-o", "json").stdout)

    def create(self, path):
        result = self.kube("create", "-f", str(path), "-o", "json", check=False)
        # kubectl can emit consecutive JSON objects for a multi-document file.
        raw = result.stdout.decode().lstrip()
        while raw:
            value, end = json.JSONDecoder().raw_decode(raw)
            for resource in value.get("items", [value]):
                self.created.append((resource["kind"], resource["metadata"]["name"],
                                     resource["metadata"]["uid"]))
            raw = raw[end:].lstrip()
        if result.returncode:
            raise RuntimeError("resource creation failed; inspect command stderr")

    def completed(self, name):
        self.cases.append(name)
        write(self.root / "completed-cases.json", self.cases)

    def wait_job(self, name, success):
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            status = self.get("job", name).get("status", {})
            if status.get("succeeded", 0):
                assert success, "generation unexpectedly succeeded"
                return
            if status.get("failed", 0):
                assert not success, "generation failed; inspect Job/Pod artifacts"
                return
            time.sleep(1)
        raise TimeoutError(f"Job {name}: inspect Pod scheduling/image/storage events")

    def render(self):
        settings = dict(line.split("=", 1) for line in
                        (self.source / "source.env.example").read_text().splitlines()
                        if line and not line.startswith("#"))
        settings.update(SOURCE_NAME=self.name, PVC_NAME=self.name,
                        NAMESPACE=self.args.namespace, SOURCE_NODE=self.args.node,
                        TOOLS_IMAGE=self.args.tools_image, FILE_SERVER_IMAGE=self.args.file_server_image,
                        PVC_STORAGE_CLASS=self.args.storage_class, PVC_CAPACITY="64Mi", B="64", N="2")
        settings["BASE_URL"] = f'http://{self.name}.{self.args.namespace}.svc.cluster.local'
        write(self.root / "settings.json", settings)
        paths = []
        variables = " ".join("${" + key + "}" for key in settings)
        for template in ("pvc.example.yaml", "dataset-job.tpl.yaml", "file-server.tpl.yaml"):
            path = self.root / template.replace(".tpl", "").replace(".example", "")
            rendered = subprocess.run(["envsubst", variables],
                                      input=(self.source / template).read_bytes(),
                                      env={**os.environ, **settings}, capture_output=True, check=True).stdout
            assert not re.search(rb"\$\{[A-Z_]+\}", rendered), template
            path.write_bytes(rendered)
            self.kube("apply", "--dry-run=server", "-f", str(path))
            paths.append(path)
        return settings, paths

    @contextmanager
    def forward(self):
        log = self.root / f"forward-{self.sequence:03}.log"
        with log.open("w") as output:
            process = subprocess.Popen(["kubectl", "-n", self.args.namespace, "port-forward",
                                        "service/" + self.name, ":80", "--address=127.0.0.1"],
                                       stdout=output, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+)", log.read_text())
                    if match:
                        yield int(match.group(1))
                        return
                    if process.poll() is not None:
                        raise RuntimeError(f"port-forward failed; inspect {log}")
                    time.sleep(0.1)
                raise TimeoutError("port-forward readiness")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)

    def http(self, port, path, method="GET", headers=None):
        client = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        try:
            client.request(method, path, headers=headers or {})
            response = client.getresponse()
            body = response.read(65)  # The fixed fixture is 64 bytes; never collect arbitrary bodies.
            facts = {"method": method, "path": path, "status": response.status,
                     "headers": dict(response.getheaders()), "received_bytes": len(body),
                     "sha256": hashlib.sha256(body).hexdigest()}
            write(self.root / f"http-{self.sequence:03}-{len(list(self.root.glob('http-*.json'))):02}.json", facts)
            return response, body
        finally:
            client.close()

    def execute(self):
        settings, (pvc, job, server) = self.render()
        self.create(pvc)
        self.create(job)
        self.wait_job(self.name + "-data", success=True)
        generation = self.get("job", self.name + "-data")["spec"]["template"]["spec"]
        assert generation["volumes"][0]["persistentVolumeClaim"]["claimName"] == settings["PVC_NAME"]
        pods = json.loads(self.kube("get", "pods", "-l",
                                   "batch.kubernetes.io/job-name=" + self.name + "-data", "-o", "json").stdout)["items"]
        assert len(pods) == 1 and pods[0]["status"]["phase"] == "Succeeded"
        raw_log = self.kube("logs", pods[0]["metadata"]["name"]).stdout
        results = []
        for line in raw_log.splitlines():
            try:
                result = json.loads(line)
            except ValueError:
                continue
            if isinstance(result, dict) and "generated_objects" in result and "manifest" in result:
                results.append(result)
        assert len(results) == 1 and results[0]["errors"] == []
        generated = results[0]
        assert generated["generated_objects"] == 2 and generated["generated_bytes"] == 128
        manifest_id = generated["manifest"]["sha256"]
        assert generated["manifest"]["path"] == f"/data/dataset/manifests/{manifest_id}/dataset-manifest.json"
        self.kube("delete", "job", self.name + "-data", "--wait=true")
        self.create(server)
        self.kube("rollout", "status", "statefulset/" + self.name, "--timeout=120s", timeout=130)
        serving = self.get("statefulset", self.name)["spec"]["template"]["spec"]
        assert serving["volumes"][0]["persistentVolumeClaim"]["claimName"] == settings["PVC_NAME"]
        mounts = {m["mountPath"]: m for m in serving["containers"][0]["volumeMounts"]}
        assert mounts["/usr/share/nginx/html/objects"]["subPath"] == "dataset/objects"
        assert mounts["/usr/share/nginx/html/objects"]["readOnly"] is True
        path = f"/fixed-pool-data/manifests/{manifest_id}/dataset-manifest.json"
        raw = self.kube("exec", self.name + "-0", "--", "cat", path).stdout
        assert hashlib.sha256(raw).hexdigest() == manifest_id
        (self.root / "dataset-manifest.json").write_bytes(raw)
        manifest = json.loads(raw)
        vectors = json.loads((self.source.parents[1] / "testdata/vectors.json").read_text())
        prefix = bytes.fromhex(vectors["prefix_hex"])
        with self.forward() as port:
            for index, obj in enumerate(manifest["objects"]):
                object_path = "/objects/sha256/" + vectors["sha256"][index]
                assert obj["url"] == settings["BASE_URL"] + object_path
                response, body = self.http(port, object_path)
                assert response.status == 200 and len(body) == 64
                assert hashlib.sha256(body).hexdigest() == vectors["sha256"][index]
                full_body = body
                if index == 0:
                    assert body[:16] == prefix
                response, body = self.http(port, object_path, "HEAD")
                assert response.status == 200 and response.getheader("Content-Length") == "64" and not body
                response, body = self.http(port, object_path, headers={"Range": "bytes=0-15"})
                assert response.status == 206 and response.getheader("Content-Range") == "bytes 0-15/64"
                assert response.getheader("Content-Length") == "16" and body == full_body[:16]
                response, _ = self.http(port, "/incorrect-prefix" + object_path)
                assert response.status == 404
        self.completed("generated-object-url-head-get-range")

        # Mutate only this run's server: wrong mount must be observed as a failed object GET.
        patch = {"spec": {"template": {"spec": {"containers": [{"name": "file-server", "volumeMounts": [
            {"name": "source", "mountPath": "/usr/share/nginx/html/objects", "subPath": "dataset", "readOnly": True}]}]}}}}
        self.kube("patch", "statefulset", self.name, "--type=strategic", "-p", json.dumps(patch))
        self.kube("rollout", "status", "statefulset/" + self.name, "--timeout=120s", timeout=130)
        with self.forward() as port:
            response, _ = self.http(port, "/objects/sha256/" + vectors["sha256"][0])
            assert response.status == 404
        self.completed("wrong-mount-is-visible")
        self.kube("apply", "-f", str(server))
        self.kube("rollout", "status", "statefulset/" + self.name, "--timeout=120s", timeout=130)
        restored = self.kube("exec", self.name + "-0", "--", "cat", path).stdout
        assert restored == raw
        with self.forward() as port:
            response, body = self.http(port, "/objects/sha256/" + vectors["sha256"][0])
            assert response.status == 200 and hashlib.sha256(body).hexdigest() == vectors["sha256"][0]
        self.completed("restore-without-regeneration")

        # A fresh Job against the nonempty root must fail without publishing a new result.
        self.create(job)
        self.wait_job(self.name + "-data", success=False)
        failed_log = self.kube("logs", "job/" + self.name + "-data").stdout
        assert b"DATA_CONFLICT" in failed_log and b'"manifest":null' in failed_log
        assert self.kube("exec", self.name + "-0", "--", "cat", path).stdout == raw
        self.completed("generation-conflict-preserves-dataset")

    def cleanup(self):
        pods = self.kube("get", "pods", "-l", "app.kubernetes.io/instance=" + self.name, "-o", "json", check=False)
        if pods.returncode == 0:
            for pod in json.loads(pods.stdout)["items"]:
                name = pod["metadata"]["name"]
                self.kube("logs", name, "--pod-running-timeout=2s", check=False)
                self.kube("describe", "pod", name, check=False)
        for kind, name, uid in reversed(self.created):
            self.kube("get", "events", "--field-selector", "involvedObject.name=" + name,
                      "-o", "json", check=False)
            current = self.kube("get", kind, name, "-o", "json", "--ignore-not-found", check=False)
            if current.returncode or not current.stdout.strip():
                continue
            assert json.loads(current.stdout)["metadata"]["uid"] == uid, "resource identity changed; not deleting"
            self.kube("delete", kind, name, "--wait=true", "--timeout=60s", timeout=70)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("work-dir", "namespace", "node", "storage-class", "tools-image", "file-server-image"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    for value in (args.tools_image, args.file_server_image):
        if not re.fullmatch(r"[^\s@]+@sha256:[a-f0-9]{64}", value):
            parser.error("images must be repository@sha256:...")
    test = Regression(args)
    try:
        test.execute()
    finally:
        test.cleanup()
    write(test.root / "result.json", {"status": "passed", "cases": test.cases})
    print("Source regression passed; artifacts:", test.root)


if __name__ == "__main__":
    main()
