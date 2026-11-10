#!/usr/bin/env python3
"""Run the Android release pipeline for new commits on the configured main branch."""
import argparse
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time

STATUS_CONTEXT = "release/android"
VERSION_BASE = 100000


def run(args, cwd=None, capture=False, env=None):
    return subprocess.run(args, cwd=cwd, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def atomic_json(path, value):
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def digest(path):
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(64 * 1024), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def gzip_file(source, destination):
    """Write source gzipped to destination, flushed before it takes the name."""
    temporary = destination.with_name(destination.name + ".tmp")
    with source.open("rb") as plain, temporary.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", compresslevel=6, fileobj=raw, mtime=0) as packed:
            shutil.copyfileobj(plain, packed, 1024 * 1024)
        raw.flush()
        os.fsync(raw.fileno())
    temporary.replace(destination)


def version_code(checkout, sha, base=VERSION_BASE):
    # First-parent history orders master merges independently of wall clocks,
    # queue timing and retries. A rewritten history is rejected before building.
    count = int(run(["git", "rev-list", "--first-parent", "--count", sha], checkout, True))
    if type(base) is not int or base < 0:
        raise ValueError("version_base must be a nonnegative integer")
    version = base + count
    if not base < version <= 2100000000:
        raise ValueError("Android version range exhausted")
    return version


def status(config, sha, state, description):
    if config.get("report_commit_status") is not True:
        print(f"GitHub commit status disabled: {STATUS_CONTEXT} {state}", flush=True)
        return
    gh = config.get("gh", "gh")
    run([gh, "api", f"repos/{config['repository']}/statuses/{sha}",
         "-f", "state=" + state, "-f", "context=" + STATUS_CONTEXT,
         "-f", "description=" + description[:140]])


def prepare(config):
    checkout = Path(config["checkout"]).expanduser().resolve()
    if not checkout.exists():
        checkout.parent.mkdir(parents=True, exist_ok=True)
        run(["git", "clone", "--no-checkout", config["git_url"], str(checkout)])
    if not (checkout / ".git").exists():
        raise ValueError("pipeline checkout is not a Git repository")
    actual = run(["git", "remote", "get-url", "origin"], checkout, True).strip()
    if actual != config["git_url"]:
        raise ValueError("pipeline origin differs from configured repository")
    branch = config.get("branch", "master")
    run(["git", "check-ref-format", "refs/heads/" + branch], checkout)
    run(["git", "fetch", "origin", f"+refs/heads/{branch}:refs/remotes/origin/{branch}"], checkout)
    sha = run(["git", "rev-parse", f"refs/remotes/origin/{branch}"], checkout, True).strip()
    return checkout, sha


def check_history(checkout, previous, sha):
    if previous:
        run(["git", "merge-base", "--is-ancestor", previous, sha], checkout)


def build(config, checkout, sha, directory):
    run(["git", "checkout", "--detach", "--force", sha], checkout)
    # The dedicated clone never contains user work. Do not remove its build caches.
    env = dict(os.environ, **config.get("build_environment", {}))
    code = version_code(checkout, sha, config.get("version_base", VERSION_BASE))
    name = f"0.1.{code}-{sha[:12]}"
    unsigned = directory / "unsigned.apk"
    receipt = directory / "build.json"
    if receipt.exists() and unsigned.exists():
        built = json.loads(receipt.read_text())
        if (built.get("repository") == config["repository"] and built.get("source_sha") == sha
                and built.get("version_code") == code and built.get("package") == config["application_id"]
                and built.get("unsigned_sha256") == digest(unsigned)):
            return built
    tests = ["python3", "-m", "unittest", "discover", "-s", "scripts/release", "-p", "test_*android*.py"]
    run(tests, checkout, env=env)
    run(["python3", "-m", "unittest", "discover", "-s", "scripts/test",
         "-p", "test_persona_script_identity.py"], checkout, env=env)
    run(["python3", "scripts/privacy_check.py"], checkout, env=env)
    outputs = checkout / "android/app/build/outputs"
    # The checks and the copy below read these: an APK or R8 output an earlier build left must never pass for this one.
    for stale in (outputs / "apk/release", outputs / "mapping/release"):
        if stale.exists():
            shutil.rmtree(stale)
    run(["./gradlew", "--no-daemon", "--console=plain",
         "-PpersonaApplicationId=" + config["application_id"],
         f"-PpersonaVersionCode={code}", "-PpersonaVersionName=" + name,
         ":core:hub:test", ":feature:settings:testDebugUnitTest",
         ":feature:agents:testDebugUnitTest", ":platform:notify:testDebugUnitTest",
         ":app:assembleRelease"], checkout / "android", env=env)
    apk = outputs / "apk/release/app-release-unsigned.apk"
    # R8 keeps only what code references; stop before signing when it dropped something reached by name.
    run(["python3", "scripts/release/check_android_shrink.py", str(apk), str(outputs / "mapping/release")],
        checkout, env=env)
    shutil.copyfile(apk, unsigned)
    unsigned_sha256 = digest(unsigned)
    # Frames of optimized code need R8's mapping to read (docs/android/android.md "Release shrinking"); it is about
    # 200 MB, gzip makes it about 12 MB. Named by the APK it belongs to, and kept beside it.
    mapping = outputs / "mapping/release/mapping.txt"
    if mapping.exists():
        gzip_file(mapping, directory / f"mapping-{unsigned_sha256}.txt.gz")
    result = dict(repository=config["repository"], source_sha=sha, unsigned_sha256=unsigned_sha256,
                  package=config["application_id"], version_code=code, version_name=name)
    atomic_json(receipt, result)
    return result


def signing(config, request, directory):
    queue = Path(config["signing_queue"]).expanduser().resolve()
    queue.mkdir(parents=True, exist_ok=True, mode=0o700)
    signed = queue / "signed.apk"
    receipt = queue / "receipt.json"
    # Publish the request last so the remote signer never sees a partial input.
    existing = json.loads((queue / "request.json").read_text()) if (queue / "request.json").exists() else None
    if existing != request:
        (queue / "request.json").unlink(missing_ok=True)
        shutil.copyfile(directory / "unsigned.apk", queue / "unsigned.tmp")
        (queue / "unsigned.tmp").replace(queue / "unsigned.apk")
        receipt.unlink(missing_ok=True)
        atomic_json(queue / "request.json", request)
    deadline = time.monotonic() + config.get("signing_timeout_seconds", 1200)
    print("Waiting for the configured signing host", flush=True)
    while time.monotonic() < deadline:
        if receipt.exists() and signed.exists():
            result = json.loads(receipt.read_text())
            if result.get("source_sha") == request["source_sha"]:
                for key in ("unsigned_sha256", "package", "version_code"):
                    if result.get(key) != request[key]:
                        raise ValueError("signing receipt identity mismatch")
                if result.get("certificate_sha256") != config["certificate_sha256"]:
                    raise ValueError("signing receipt certificate mismatch")
                if result.get("signed_sha256") != digest(signed):
                    raise ValueError("signing receipt digest mismatch")
                shutil.copyfile(signed, directory / "signed.apk")
                atomic_json(directory / "signature.json", result)
                return
        time.sleep(5)
    raise TimeoutError("signing host did not return a verified APK before the deadline")


def publish(config, checkout, request, directory):
    env = dict(os.environ, **config.get("build_environment", {}))
    command = ["python3", "scripts/release/publish_android_apk.py", str(directory / "signed.apk"),
               "--source-sha", request["source_sha"], "--version-code", str(request["version_code"]),
               "--version-name", request["version_name"], "--expected-package", config["application_id"],
               "--expected-cert-sha256", config["certificate_sha256"],
               "--hub-ssh", config["hub_ssh"], "--remote-dir", config["remote_directory"]]
    if config.get("ssh_command"):
        command += ["--ssh-command", json.dumps(config["ssh_command"])]
    run(command, checkout, env=env)
    # This private adapter must GET latest and the complete APK over verified TLS,
    # check authentication rejection, then verify hash and certificate independently.
    run(config["verify_command"] + [str(directory / "signature.json")], checkout, env=env)


def finish_verified(config, directory, sha, code, complete):
    """Retry only the status/checkpoint after the full publication verifier succeeded."""
    verified = json.loads((directory / "verified.json").read_text())
    request = verified["request"]
    if request.get("repository") != config["repository"] or request.get("source_sha") != sha:
        raise ValueError("verified receipt belongs to a different repository or commit")
    if (request.get("package") != config["application_id"]
            or request.get("version_code") != code
            or verified.get("certificate_sha256") != config["certificate_sha256"]
            or verified.get("signed_sha256") != digest(directory / "signed.apk")):
        raise ValueError("verified artifact identity changed")
    status(config, sha, "success", "Tests, signed Release APK and authenticated TLS Hub download verified")
    atomic_json(complete, request)
    print("Published " + sha)


def recovery_request(config, checkout, sha, directory, code):
    """Validate the existing artifact before a verification-only retry."""
    if run(["git", "remote", "get-url", "origin"], checkout, True).strip() != config["git_url"]:
        raise ValueError("recovery repository mismatch")
    if run(["git", "rev-parse", "HEAD"], checkout, True).strip() != sha:
        raise ValueError("recovery checkout must retain the artifact source")
    request = json.loads((directory / "build.json").read_text())
    signature = json.loads((directory / "signature.json").read_text())
    expected = dict(repository=config["repository"], source_sha=sha,
                    package=config["application_id"], version_code=code)
    if any(request.get(k) != v or signature.get(k) != v for k, v in expected.items()):
        raise ValueError("recovery artifact identity mismatch")
    if (signature.get("certificate_sha256") != config["certificate_sha256"]
            or signature.get("signed_sha256") != digest(directory / "signed.apk")
            or signature.get("unsigned_sha256") != request.get("unsigned_sha256")):
        raise ValueError("recovery artifact digest or certificate mismatch")
    return request


def cycle(config, verify_source=None):
    state = Path(config["state_directory"]).expanduser().resolve()
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (state / "worker.lock").open("a") as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("Another Android release is already running")
            return
        if verify_source is None:
            checkout, sha = prepare(config)
        else:
            if not re.fullmatch(r"[0-9a-f]{40}", verify_source):
                raise ValueError("verification source must be a complete commit SHA")
            checkout, sha = Path(config["checkout"]).expanduser().resolve(), verify_source
        complete = state / "published.json"
        previous = json.loads(complete.read_text()) if complete.exists() else {}
        if previous and previous.get("repository") != config["repository"]:
            raise ValueError("checkpoint repository mismatch; migrate to a separate state directory")
        if previous.get("source_sha") == sha:
            print("Already published " + sha)
            return
        check_history(checkout, previous.get("source_sha"), sha)
        directory = state / "runs" / sha
        directory.mkdir(parents=True, exist_ok=True)
        code = version_code(checkout, sha, config.get("version_base", VERSION_BASE))
        if previous and code <= previous["version_code"]:
            raise ValueError("new source version must exceed the published version")
        if (directory / "verified.json").exists():
            finish_verified(config, directory, sha, code, complete)
            return
        if verify_source is None and (directory / "failure.json").exists():
            # A timer tick is not authorization to retry the same failed artifact.
            # Leave its failure status and receipts intact; new commits are independent.
            print("Previous release failed for " + sha + "; explicit recovery or a new source is required")
            return
        status(config, sha, "pending", "Android tests, release build, signing and Hub publication")
        try:
            with (directory / "release.log").open("a", buffering=1) as log:
                # Child commands and Python diagnostics share a durable per-source log.
                stdout, stderr = os.dup(1), os.dup(2)
                try:
                    sys.stdout.flush(); sys.stderr.flush()
                    os.dup2(log.fileno(), 1); os.dup2(log.fileno(), 2)
                    print("SOURCE_SHA=" + sha, flush=True)
                    if verify_source is None:
                        request = build(config, checkout, sha, directory)
                        signing(config, request, directory)
                        publish(config, checkout, request, directory)
                    else:
                        request = recovery_request(config, checkout, sha, directory, code)
                        # Preserve the failed attempt before the adapter replaces its reports.
                        attempt = directory / ("verification-retry-" + str(time.time_ns()))
                        attempt.mkdir()
                        for name in ("failure.json", "android-live-test-attempt.xml"):
                            if (directory / name).exists():
                                shutil.copyfile(directory / name, attempt / name)
                        env = dict(os.environ, **config.get("build_environment", {}))
                        env["PERSONA_ANDROID_VERIFY_ONLY"] = "1"
                        run(config["verify_command"] + [str(directory / "signature.json")], checkout, env=env)
                    signature = json.loads((directory / "signature.json").read_text())
                    atomic_json(directory / "verified.json", dict(
                        request=request, signed_sha256=signature["signed_sha256"],
                        certificate_sha256=signature["certificate_sha256"]))
                    print("ANDROID_RELEASE_SUCCESS " + sha, flush=True)
                finally:
                    sys.stdout.flush(); sys.stderr.flush()
                    os.dup2(stdout, 1); os.dup2(stderr, 2)
                    os.close(stdout); os.close(stderr)
            finish_verified(config, directory, sha, code, complete)
        except Exception as error:
            atomic_json(directory / "failure.json", {"source_sha": sha, "error": str(error)})
            if not (directory / "verified.json").exists():
                status(config, sha, "failure", "Android release failed; inspect worker log and Hub metadata")
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--verify-source", help="Retry only verification for an existing signed source; never fetch, build, sign or publish")
    args = parser.parse_args()
    os.umask(0o077)
    config = json.loads(args.config.read_text())
    if not re.fullmatch(r"[0-9a-f]{64}", config["certificate_sha256"]):
        parser.error("certificate_sha256 must be the existing signing certificate digest")
    cycle(config, args.verify_source)


if __name__ == "__main__":
    main()
