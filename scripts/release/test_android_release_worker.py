"""Release orchestration invariants independent of machine-specific credentials."""
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("worker", Path(__file__).with_name("android_release_worker.py"))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class ReleaseWorkerTest(unittest.TestCase):
    def test_digest_empty_and_multichunk_binary_files(self):
        for data in (b"", bytes(range(256)) * 513 + b"\x00\xff\x80"):
            with self.subTest(size=len(data)), tempfile.TemporaryDirectory() as tmp:
                path = Path(tmp) / "artifact.bin"
                path.write_bytes(data)
                self.assertEqual(worker.digest(path), hashlib.sha256(data).hexdigest())

    def test_versions_follow_main_history_and_retries_are_stable(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, text=True).strip()
            git("init", "-q")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "first")
            first = git("rev-parse", "HEAD")
            code = worker.version_code(root, first)
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "second")
            second = git("rev-parse", "HEAD")
            self.assertEqual(code, worker.version_code(root, first))
            self.assertEqual(code + 1, worker.version_code(root, second))
            self.assertEqual(200002, worker.version_code(root, second, 200000))
            for invalid in (-1, True, "200000", 2100000000):
                with self.assertRaises(ValueError):
                    worker.version_code(root, second, invalid)
            worker.check_history(root, first, second)
            with self.assertRaises(subprocess.CalledProcessError):
                worker.check_history(root, second, first)

    def test_failure_retains_successful_state_and_reports_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            previous = {"repository": "example/app", "source_sha": "a" * 40, "version_code": 100001}
            worker.atomic_json(root / "published.json", previous)
            config = {"state_directory": tmp, "repository": "example/app"}
            with patch.object(worker, "prepare", return_value=(root, "b" * 40)), \
                    patch.object(worker, "version_code", return_value=100002), \
                    patch.object(worker, "check_history"), patch.object(worker, "status") as status, \
                    patch.object(worker, "build", side_effect=RuntimeError("test build failure")), \
                    patch.object(worker, "signing") as signing:
                with self.assertRaisesRegex(RuntimeError, "test build failure"):
                    worker.cycle(config)
                self.assertEqual([call.args[2] for call in status.call_args_list], ["pending", "failure"])
                signing.assert_not_called()
            self.assertEqual(json.loads((root / "published.json").read_text()), previous)

    def test_signing_receipt_rejects_wrong_identity_and_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            request = dict(source_sha="a" * 40, unsigned_sha256="b" * 64, package="com.example.app", version_code=2)
            worker.atomic_json(root / "request.json", request)
            (root / "signed.apk").write_bytes(b"signed")
            config = dict(signing_queue=tmp, certificate_sha256="c" * 64, signing_timeout_seconds=1)
            for changes in ({"package": "com.example.other"}, {"signed_sha256": "d" * 64},
                            {"certificate_sha256": "e" * 64}):
                receipt = dict(request, signed_sha256=worker.digest(root / "signed.apk"), certificate_sha256="c" * 64)
                receipt.update(changes)
                worker.atomic_json(root / "receipt.json", receipt)
                with self.assertRaises(ValueError):
                    worker.signing(config, request, root)

    def test_already_published_does_not_build_or_publish_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            worker.atomic_json(root / "published.json", {"repository": "example/app", "source_sha": "a" * 40})
            with patch.object(worker, "prepare", return_value=(root, "a" * 40)), \
                    patch.object(worker, "build") as build:
                worker.cycle({"state_directory": tmp, "repository": "example/app"})
                build.assert_not_called()

    def test_foreign_or_unscoped_checkpoint_never_writes_status(self):
        for repository in (None, "example/previous"):
            with self.subTest(repository=repository), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                worker.atomic_json(root / "published.json", dict(repository=repository, source_sha="a" * 40))
                with patch.object(worker, "prepare", return_value=(root, "a" * 40)), \
                        patch.object(worker, "status") as status:
                    with self.assertRaisesRegex(ValueError, "checkpoint repository mismatch"):
                        worker.cycle(dict(state_directory=tmp, repository="example/app"))
                    status.assert_not_called()

    def test_status_retry_preserves_verified_artifact_without_republishing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sha = "b" * 40
            directory = root / "runs" / sha
            directory.mkdir(parents=True)
            config = dict(state_directory=tmp, repository="example/app",
                          application_id="com.example.app", certificate_sha256="c" * 64, report_commit_status=True)
            previous = dict(repository=config["repository"], source_sha="a" * 40, version_code=100001)
            worker.atomic_json(root / "published.json", previous)
            request = dict(repository=config["repository"], source_sha=sha,
                           package=config["application_id"], version_code=100002)
            (directory / "signed.apk").write_bytes(b"verified fixture")
            worker.atomic_json(directory / "signature.json", dict(
                signed_sha256=worker.digest(directory / "signed.apk"), certificate_sha256="c" * 64))
            with patch.object(worker, "prepare", return_value=(root, sha)), \
                    patch.object(worker, "check_history"), \
                    patch.object(worker, "version_code", return_value=100002), \
                    patch.object(worker, "build", return_value=request) as build, \
                    patch.object(worker, "signing") as signing, \
                    patch.object(worker, "publish") as publish, \
                    patch.object(worker, "status", side_effect=[None, RuntimeError("status denied")]) as status:
                with self.assertRaisesRegex(RuntimeError, "status denied"):
                    worker.cycle(config)
                self.assertEqual(json.loads((root / "published.json").read_text()), previous)
                self.assertTrue((directory / "verified.json").exists())
                self.assertEqual([c.args[2] for c in status.call_args_list], ["pending", "success"])
                status.side_effect = None
                status.reset_mock()
                worker.cycle(config)
                self.assertEqual(json.loads((root / "published.json").read_text()), request)
                self.assertEqual([c.args[2] for c in status.call_args_list], ["success"])
                build.assert_called_once()
                signing.assert_called_once()
                publish.assert_called_once()
            (directory / "signed.apk").write_bytes(b"tampered")
            with patch.object(worker, "status") as status:
                with self.assertRaisesRegex(ValueError, "identity changed"):
                    worker.finish_verified(config, directory, sha, 100002, root / "published.json")
                status.assert_not_called()

    def test_verification_only_never_rebuilds_or_republishes(self):
        for fails in (True, False):
            with self.subTest(fails=fails), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                sha = "b" * 40
                directory = root / "runs" / sha
                directory.mkdir(parents=True)
                request = dict(repository="example/app", source_sha=sha,
                               package="com.example.app", version_code=100002)
                (directory / "signed.apk").write_bytes(b"signed")
                worker.atomic_json(directory / "signature.json", dict(
                    signed_sha256=worker.digest(directory / "signed.apk"), certificate_sha256="c" * 64))
                (directory / "android-live-test-attempt.xml").write_text("original failure")
                worker.atomic_json(directory / "failure.json", dict(source_sha=sha, error="probe failed"))
                config = dict(state_directory=tmp, checkout=tmp, repository="example/app",
                              application_id="com.example.app", certificate_sha256="c" * 64,
                              verify_command=["verifier"])
                with patch.object(worker, "prepare") as prepare, \
                        patch.object(worker, "version_code", return_value=100002), \
                        patch.object(worker, "recovery_request", return_value=request), \
                        patch.object(worker, "build") as build, patch.object(worker, "signing") as signing, \
                        patch.object(worker, "publish") as publish, patch.object(worker, "status"), \
                        patch.object(worker, "run", side_effect=RuntimeError("probe failed") if fails else None):
                    if fails:
                        with self.assertRaisesRegex(RuntimeError, "probe failed"):
                            worker.cycle(config, sha)
                    else:
                        worker.cycle(config, sha)
                    for operation in (prepare, build, signing, publish):
                        operation.assert_not_called()
                self.assertEqual((directory / "verified.json").exists(), not fails)
                self.assertEqual((root / "published.json").exists(), not fails)
                preserved = list(directory.glob("verification-retry-*/android-live-test-attempt.xml"))
                self.assertEqual(preserved[0].read_text(), "original failure")

    def test_timer_skips_failed_source_without_overwriting_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sha = "b" * 40
            directory = root / "runs" / sha
            directory.mkdir(parents=True)
            failure = dict(source_sha=sha, error="verification failed")
            worker.atomic_json(directory / "failure.json", failure)
            with patch.object(worker, "prepare", return_value=(root, sha)), \
                    patch.object(worker, "version_code", return_value=100002), \
                    patch.object(worker, "status") as status, \
                    patch.object(worker, "build") as build, \
                    patch.object(worker, "signing") as signing, \
                    patch.object(worker, "publish") as publish:
                worker.cycle(dict(state_directory=tmp, repository="example/app"))
                for operation in (status, build, signing, publish):
                    operation.assert_not_called()
            self.assertEqual(json.loads((directory / "failure.json").read_text()), failure)
            self.assertFalse((root / "published.json").exists())
            self.assertFalse((directory / "verified.json").exists())

    def test_failure_of_an_older_source_does_not_block_new_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            failed = root / "runs" / ("a" * 40)
            failed.mkdir(parents=True)
            worker.atomic_json(failed / "failure.json", dict(source_sha="a" * 40, error="old failure"))
            with patch.object(worker, "prepare", return_value=(root, "b" * 40)), \
                    patch.object(worker, "version_code", return_value=100002), \
                    patch.object(worker, "status"), \
                    patch.object(worker, "build", side_effect=RuntimeError("new source reached build")) as build:
                with self.assertRaisesRegex(RuntimeError, "new source reached build"):
                    worker.cycle(dict(state_directory=tmp, repository="example/app"))
                build.assert_called_once()

    def test_recovery_rejects_wrong_source_repository_certificate_and_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = dict(repository="example/app", git_url="fixture", application_id="com.example.app",
                          certificate_sha256="c" * 64)
            request = dict(repository="example/app", source_sha="a" * 40, package="com.example.app",
                           version_code=100001, unsigned_sha256="d" * 64)
            (root / "signed.apk").write_bytes(b"signed")
            worker.atomic_json(root / "build.json", request)
            signature = dict(request, certificate_sha256="c" * 64,
                             signed_sha256=worker.digest(root / "signed.apk"))
            for changes in ({"repository": "wrong"}, {"source_sha": "b" * 40},
                            {"certificate_sha256": "e" * 64}, {"signed_sha256": "f" * 64}):
                worker.atomic_json(root / "signature.json", dict(signature, **changes))
                with patch.object(worker, "run", side_effect=["fixture", "a" * 40]):
                    with self.assertRaises(ValueError):
                        worker.recovery_request(config, root, "a" * 40, root, 100001)
            worker.atomic_json(root / "signature.json", signature)
            with patch.object(worker, "run", side_effect=["fixture", "a" * 40]):
                self.assertEqual(worker.recovery_request(config, root, "a" * 40, root, 100001), request)

    def test_build_reads_only_its_own_outputs_and_keeps_the_mapping_by_apk_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = root / "runs" / ("a" * 40)
            directory.mkdir(parents=True)
            outputs = root / "android/app/build/outputs"
            apk = outputs / "apk/release/app-release-unsigned.apk"
            mapping = outputs / "mapping/release/mapping.txt"
            usage = outputs / "mapping/release/usage.txt"
            for path in (apk, mapping, usage):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("an earlier build")
            built = {apk: b"this build's APK", mapping: b"persona.A -> persona.A:\n"}

            def run(args, cwd=None, capture=False, env=None):
                if args[0] == "./gradlew":
                    # A build that fails or writes no R8 output leaves nothing older behind for the check to read.
                    self.assertFalse(apk.parent.exists() or mapping.parent.exists())
                    for path, data in built.items():
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_bytes(data)

            config = dict(repository="example/app", application_id="com.example.app")
            with patch.object(worker, "run", side_effect=run) as calls, \
                    patch.object(worker, "version_code", return_value=100002):
                result = worker.build(config, root, "a" * 40, directory)
            self.assertIn("scripts/release/check_android_shrink.py", calls.call_args_list[-1].args[0])
            self.assertEqual(result["unsigned_sha256"], hashlib.sha256(built[apk]).hexdigest())
            self.assertEqual((directory / "unsigned.apk").read_bytes(), built[apk])
            archived = directory / f"mapping-{result['unsigned_sha256']}.txt.gz"
            self.assertEqual(gzip.decompress(archived.read_bytes()), built[mapping])
            self.assertEqual(sorted(p.name for p in directory.iterdir()), sorted(["build.json", "unsigned.apk", archived.name]))

    def test_new_source_cannot_decrease_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            worker.atomic_json(root / "published.json", dict(
                repository="example/app", source_sha="a" * 40, version_code=200001))
            with patch.object(worker, "prepare", return_value=(root, "b" * 40)), \
                    patch.object(worker, "check_history"), \
                    patch.object(worker, "version_code", return_value=100001), \
                    patch.object(worker, "status") as status:
                with self.assertRaisesRegex(ValueError, "must exceed"):
                    worker.cycle(dict(state_directory=tmp, repository="example/app"))
                status.assert_not_called()


if __name__ == "__main__":
    unittest.main()
