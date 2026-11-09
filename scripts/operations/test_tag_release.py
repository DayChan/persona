"""The private tag consumer never broadens signing credentials or public refs."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import tag_release


class TagReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="tag-release-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "release@example.com")
        self.git("config", "user.name", "Fixture")
        (self.repo / "README.md").write_text("fixture\n")
        self.git("add", "README.md")
        self.git("commit", "-q", "-m", "source")
        self.commit = self.git("rev-parse", "HEAD")
        self.config_path = self.root / "release.json"
        self.config_path.write_text(json.dumps({
            "repo": str(self.repo),
            "worktree": str(self.root / "release-worktree"),
            "state_dir": str(self.root / "state"),
            "repository": "example/project",
            "platform": "android",
            "command": ["/usr/bin/true", "{commit}", "{tag}"],
            "receipt": str(self.root / "state" / "artifacts.json"),
        }))
        self.config_path.chmod(0o600)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True).strip()

    def config(self):
        return tag_release.load_config(self.config_path)

    def test_config_is_private_and_commands_are_argv_with_known_fields(self):
        config = self.config()
        self.assertEqual(config["platform"], "android")
        self.config_path.chmod(0o644)
        with self.assertRaisesRegex(tag_release.ConfigError, "0600"):
            self.config()
        self.config_path.chmod(0o600)
        for value in ("true; {commit}", ["/usr/bin/true", "{unknown}"], ["/usr/bin/true", "{commit!r}"]):
            data = json.loads(self.config_path.read_text())
            data["command"] = value
            self.config_path.write_text(json.dumps(data))
            with self.assertRaises(tag_release.ConfigError):
                self.config()

    def test_state_must_stay_outside_the_dedicated_worktree(self):
        data = json.loads(self.config_path.read_text())
        data["state_dir"] = str(self.root / "release-worktree" / "state")
        self.config_path.write_text(json.dumps(data))
        with self.assertRaisesRegex(tag_release.ConfigError, "state directory"):
            self.config()

    def test_existing_clean_detached_worktree_checks_out_exact_tag(self):
        other = self.root / "release-worktree"
        self.git("worktree", "add", "--detach", str(other), self.commit)
        (self.repo / "next").write_text("new\n")
        self.git("add", "next")
        self.git("commit", "-q", "-m", "later source")
        newer = self.git("rev-parse", "HEAD")
        tag_release.prepare_worktree(self.config(), self.commit)
        self.assertEqual(subprocess.check_output(["git", "-C", str(other), "rev-parse", "HEAD"], text=True).strip(), self.commit)
        self.assertNotEqual(newer, self.commit)
        (other / "uncommitted").write_text("do not delete\n")
        with self.assertRaisesRegex(tag_release.ConfigError, "not clean"):
            tag_release.prepare_worktree(self.config(), newer)
        self.assertEqual((other / "uncommitted").read_text(), "do not delete\n")

    def test_lightweight_and_annotated_remote_tags_resolve_to_their_commit(self):
        remote = f"{self.commit}\trefs/tags/v1.0.0\n"
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote]) as call:
            self.assertEqual(tag_release.resolve_tag(self.repo, "example/project", "v1.0.0"), self.commit)
            self.assertEqual(call.call_args_list[3].args[1:], ("ls-remote", "--tags", "https://github.com/example/project.git",
                                                               "refs/tags/v1.0.0", "refs/tags/v1.0.0^{}"))
        annotated = "a" * 40
        remote = f"{annotated}\trefs/tags/v1.0.0\n{self.commit}\trefs/tags/v1.0.0^{{}}\n"
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote]):
            self.assertEqual(tag_release.resolve_tag(self.repo, "example/project", "v1.0.0"), self.commit)
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote + "different\n"]):
            with self.assertRaisesRegex(tag_release.ConfigError, "moved"):
                tag_release.resolve_tag(self.repo, "example/project", "v1.0.0")
        with patch.object(tag_release, "git") as call:
            for invalid in ("main", "v1.0.0^{}", "v1..0", "v1.0.0;push"):
                with self.assertRaises(tag_release.ConfigError):
                    tag_release.resolve_tag(self.repo, "example/project", invalid)
            call.assert_not_called()

    def test_receipt_requires_commit_and_matching_artifact_digest(self):
        artifact = self.root / "signed.apk"
        artifact.write_bytes(b"verified APK")
        digest = tag_release.hashlib.sha256(artifact.read_bytes()).hexdigest()
        receipt = self.root / "receipt.json"
        receipt.write_text(json.dumps({"commit": self.commit, "assets": [{"path": str(artifact), "sha256": digest}]}))
        assets = tag_release.read_receipt(receipt, self.commit)
        self.assertEqual(assets, [{"path": str(artifact), "name": "signed.apk"}])
        artifact.write_bytes(b"replaced")
        with self.assertRaisesRegex(tag_release.ConfigError, "digest"):
            tag_release.read_receipt(receipt, self.commit)
        receipt.write_text(json.dumps({"commit": self.commit, "assets": []}))
        with self.assertRaisesRegex(tag_release.ConfigError, "store_verified"):
            tag_release.read_receipt(receipt, self.commit)
        receipt.write_text(json.dumps({"commit": self.commit, "store_verified": True, "assets": []}))
        self.assertEqual(tag_release.read_receipt(receipt, self.commit), [])

    def test_store_only_receipt_checks_release_without_uploading_an_artifact(self):
        with patch.object(tag_release.subprocess, "check_output", return_value=json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})) as view, patch.object(tag_release.subprocess, "run") as upload:
            tag_release.upload("example/project", "v1.0.0", [])
            upload.assert_not_called()
            self.assertEqual(view.call_args.args[0][:3], ["gh", "release", "view"])

    def test_upload_views_existing_release_and_verifies_remote_digests(self):
        artifact = self.root / "Persona-1.0.0.apk"
        artifact.write_bytes(b"signed")
        assets = [{"path": str(artifact), "name": artifact.name}]
        digest = tag_release.hashlib.sha256(artifact.read_bytes()).hexdigest()
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0)

        def output(command, **kwargs):
            calls.append(command)
            if command[-1] == "tagName,isDraft,assets":
                return json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})
            return json.dumps([{"name": artifact.name, "digest": "sha256:" + digest}])

        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=output):
            tag_release.upload("example/project", "v1.0.0", assets)
        self.assertEqual(calls[0][:3], ["gh", "release", "view"])
        self.assertEqual(calls[1][:3], ["gh", "release", "upload"])
        self.assertNotIn("create", calls[1])
        self.assertTrue(calls[1][-1].endswith("/" + artifact.name))
        self.assertNotIn("#", calls[1][-1])
        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=[json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []}), "[]"]):
            with self.assertRaisesRegex(tag_release.ConfigError, "verification"):
                tag_release.upload("example/project", "v1.0.0", assets)

    def test_failed_command_never_uploads_or_marks_complete(self):
        data = json.loads(self.config_path.read_text())
        data["command"] = ["/usr/bin/false"]
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), patch.object(tag_release, "upload") as upload:
            with self.assertRaises(subprocess.CalledProcessError):
                tag_release.publish_once(config, "v1.0.0", self.commit)
            upload.assert_not_called()
        state = json.loads((config["state_dir"] / "state.json").read_text())
        self.assertEqual(state["phase"], "publishing")
        self.assertNotIn("complete", state.values())

    def test_complete_same_tag_is_not_published_twice(self):
        config = self.config()
        tag_release.write_state(config["state_dir"] / "state.json", {
            "tag": "v1.0.0", "commit": self.commit, "phase": "complete",
        })
        with patch.object(tag_release, "prepare_worktree") as prepare:
            tag_release.publish_once(config, "v1.0.0", self.commit)
            prepare.assert_not_called()

    def test_agent_runs_under_lock_before_the_deterministic_publisher(self):
        data = json.loads(self.config_path.read_text())
        data["agent"] = ["claude", "--print", "{prompt_file}", "{log_file}"]
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        events = []

        def run(command, **kwargs):
            events.append(command)
            self.assertEqual(command[-2:], [str(Path(tag_release.__file__).with_name("tag_release_prompt.md")), str(config["state_dir"] / "agent.log")])
            self.assertEqual(kwargs["env"]["PERSONA_RELEASE_COMMIT"], self.commit)
            self.assertEqual(kwargs["env"]["PERSONA_RELEASE_PLATFORM"], "android")
            return subprocess.CompletedProcess(command, 0)

        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: events.append("prepared")), patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release, "publish_once", side_effect=lambda *args: events.append("published")):
            tag_release.invoke_agent(config, "v1.0.0", self.commit)
        self.assertEqual(events[0], "prepared")
        self.assertEqual(events[-1], "published")
        self.assertEqual(len(events), 3)

    def test_tick_skips_only_the_same_platform_tag_and_keeps_lock(self):
        config = self.config()
        tag_release.write_state(config["state_dir"] / "state.json", {"tag": "v0.9.0", "commit": self.commit, "phase": "complete"})
        with patch.object(tag_release, "latest_tags", return_value=["v0.9.0", "v1.0.0"]), patch.object(tag_release, "resolve_tag", return_value=self.commit), patch.object(tag_release, "invoke_agent") as invoke:
            tag_release.tick(config)
            invoke.assert_called_once_with(config, "v1.0.0", self.commit)

    def test_prompt_template_does_not_require_a_private_config(self):
        prompt = (Path(tag_release.__file__).with_name("tag_release_prompt.md")).read_text()
        self.assertIn("--once", prompt)
        self.assertIn("store_verified", prompt)
        self.assertIn("<owner>/<name>", prompt)
        self.assertIn("frozen native Mac", prompt)


if __name__ == "__main__":
    unittest.main()
