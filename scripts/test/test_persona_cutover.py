#!/usr/bin/env python3
"""Offline cutover checks. All writes stay inside newly allocated temporary homes."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve().parents[1] / "migrate" / "persona-cutover.py"
SPEC = importlib.util.spec_from_file_location("persona_cutover", SCRIPT)
CUTOVER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CUTOVER)
SOURCE = "previous"


class CutoverTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-cutover-test-")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name).resolve()
        self.old = self.home / ("." + SOURCE)
        self.new = self.home / ".persona"
        self.journal = self.home / ".persona-cutover-test"

    def write(self, relative, content="fixture", mode=0o600):
        path = self.home / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        path.chmod(mode)
        return path

    def old_store(self):
        self.write(self.old.name + "/hub.json", json.dumps({"token": "sandbox-secret", "notes": SOURCE}), 0o640)
        self.write(self.old.name + "/secret.key", "sandbox-key", 0o644)
        self.write(self.old.name + "/data/messages.jsonl", json.dumps({"text": SOURCE, "url": "/j/demo"}))
        self.write(self.old.name + "/bin/" + SOURCE + "d", "old-hub-binary", 0o755)
        self.write(".config/" + SOURCE + "/config.json", json.dumps({"app": {"bundle_id": "com.example." + SOURCE}}), 0o640)
        self.old.chmod(0o755)

    def cli(self, *args):
        return subprocess.run([sys.executable, "-I", str(SCRIPT), "--source-name", SOURCE,
                               "--home", str(self.home), *map(str, args)], text=True,
                              capture_output=True, timeout=15, cwd=str(SCRIPT.parent))

    def apply_cli(self, *args):
        return self.cli("--apply", "--approve", "--services-stopped", "--backup-verified",
                        "--journal", self.journal, *args)

    def rollback_cli(self):
        return self.cli("--rollback", "--approve", "--services-stopped", "--journal", self.journal)

    def journal_data(self):
        return json.loads((self.journal / "journal.json").read_text())

    def test_plan_is_read_only_and_does_not_print_secrets(self):
        self.old_store()
        before = CUTOVER.snapshot(self.old / "hub.json")
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["verdict"], "plan")
        self.assertNotIn("sandbox-secret", result.stdout + result.stderr)
        self.assertEqual(CUTOVER.snapshot(self.old / "hub.json"), before)
        self.assertFalse(self.new.exists())
        self.assertFalse(self.journal.exists())
        self.assertFalse((self.home / ".persona-cutover.lock").exists())

    def test_explicit_approvals_are_required(self):
        self.old_store()
        for missing in ("--approve", "--services-stopped", "--backup-verified", "--journal"):
            flags = ["--apply", "--approve", "--services-stopped", "--backup-verified", "--journal", str(self.journal)]
            index = flags.index(missing)
            del flags[index:index + (2 if missing == "--journal" else 1)]
            result = self.cli(*flags)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertTrue(self.old.exists())
            self.assertFalse(self.journal.exists())

    def test_source_name_is_required_distinct_and_not_shell_syntax(self):
        for source in ("persona", "PERSONA", "../outside", "x;touch-file", "Upper", "", "a\ncommand"):
            for action in ([], ["--print-database-sql"]):
                result = subprocess.run([sys.executable, "-I", str(SCRIPT), "--source-name", source,
                                         "--home", str(self.home), *action], capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, "")
        result = subprocess.run([sys.executable, "-I", str(SCRIPT), "--home", str(self.home)],
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 2)

    def test_apply_and_rollback_preserve_bytes_ids_and_permissions(self):
        self.old_store()
        old_info = CUTOVER.snapshot(self.old)
        token_info = CUTOVER.snapshot(self.old / "hub.json")
        messages = (self.old / "data/messages.jsonl").read_bytes()
        self.assertEqual(self.apply_cli().returncode, 0)
        self.assertFalse(CUTOVER.present(self.old))
        self.assertFalse(CUTOVER.present(self.home / ".config" / SOURCE))
        self.assertTrue((self.new / "bin/personad").exists())
        self.assertEqual((self.new / "data/messages.jsonl").read_bytes(), messages)
        self.assertEqual(CUTOVER.snapshot(self.new)["inode"], old_info["inode"])
        self.assertEqual(CUTOVER.snapshot(self.new / "hub.json")["inode"], token_info["inode"])
        self.assertEqual(stat.S_IMODE(self.new.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((self.new / "secret.key").stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.journal.stat().st_mode), 0o700)
        for file in self.journal.iterdir():
            self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o600)
        backup = next(op["backup"] for op in self.journal_data()["operations"] if op.get("backup_binary"))
        self.assertEqual((self.journal / backup).read_bytes(), b"old-hub-binary")
        result = self.rollback_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(CUTOVER.snapshot(self.old), old_info)
        self.assertEqual(CUTOVER.snapshot(self.old / "hub.json"), token_info)
        config = json.loads((self.home / ".config" / SOURCE / "config.json").read_text())
        self.assertEqual(config["app"]["bundle_id"], "com.example." + SOURCE)
        self.assertFalse(CUTOVER.present(self.new))
        self.assertEqual(self.journal_data()["status"], "rolled_back")
        self.assertEqual(self.rollback_cli().returncode, 0)

    def test_empty_machine_does_not_create_stores(self):
        self.assertEqual(json.loads(self.cli().stdout)["operations"], [])
        self.assertEqual(list(self.home.iterdir()), [])

    def test_new_only_store_is_not_duplicated(self):
        self.write(".persona/hub.json", "{}")
        self.new.chmod(0o700)
        self.assertEqual(json.loads(self.cli().stdout)["operations"], [])
        self.assertFalse(CUTOVER.present(self.old))

    def test_two_real_directories_are_refused_even_when_empty(self):
        self.old.mkdir()
        self.new.mkdir()
        result = self.apply_cli()
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.journal.exists())
        self.assertTrue(self.old.exists() and self.new.exists())

    def test_config_conflict_preflights_before_runtime_move(self):
        self.old_store()
        self.write(".config/persona/config.json", "{}")
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertTrue(self.old.exists())
        self.assertFalse(self.new.exists())
        self.assertFalse(self.journal.exists())

    @unittest.skipIf(os.name == "nt", "symlink privileges differ on Windows")
    def test_destination_alias_to_source_is_removed_then_restored_on_rollback(self):
        self.old_store()
        self.new.symlink_to(self.old.name, target_is_directory=True)
        result = self.apply_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.new.is_symlink())
        self.assertFalse(CUTOVER.present(self.old))
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue(self.new.is_symlink())
        self.assertEqual(os.readlink(self.new), self.old.name)

    @unittest.skipIf(os.name == "nt", "symlink privileges differ on Windows")
    def test_source_alias_to_new_is_removed_without_moving_real_store(self):
        self.write(".persona/hub.json", "{}")
        self.old.symlink_to(self.new.name, target_is_directory=True)
        inode = self.new.stat().st_ino
        self.assertEqual(self.apply_cli().returncode, 0)
        self.assertEqual(self.new.stat().st_ino, inode)
        self.assertFalse(CUTOVER.present(self.old))
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue(self.old.is_symlink())

    @unittest.skipIf(os.name == "nt", "symlink privileges differ on Windows")
    def test_external_alias_dangling_alias_and_loop_are_refused(self):
        for variant in ("external", "dangling", "loop"):
            with self.subTest(variant=variant):
                outside = self.home / "external"
                outside.mkdir(exist_ok=True)
                if variant == "loop":
                    self.old.symlink_to(self.new.name, target_is_directory=True)
                    self.new.symlink_to(self.old.name, target_is_directory=True)
                else:
                    self.old.symlink_to(outside if variant == "external" else self.home / "missing")
                self.assertEqual(self.apply_cli().returncode, 1)
                self.assertFalse(self.journal.exists())
                self.old.unlink()
                if self.new.is_symlink():
                    self.new.unlink()

    @unittest.skipIf(os.name == "nt", "symlink privileges differ on Windows")
    def test_config_parent_and_binary_parent_symlinks_are_refused(self):
        self.old_store()
        bin_dir = self.old / "bin"
        bin_dir.rename(self.home / "external-bin")
        bin_dir.symlink_to(self.home / "external-bin", target_is_directory=True)
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertFalse(self.journal.exists())
        bin_dir.unlink()
        (self.home / "external-bin").rename(bin_dir)
        (self.home / ".config").rename(self.home / "external-config")
        (self.home / ".config").symlink_to(self.home / "external-config", target_is_directory=True)
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertFalse(self.journal.exists())

    def test_existing_binary_target_is_refused(self):
        self.old_store()
        self.write(self.old.name + "/bin/personad", "other-binary", 0o755)
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertFalse(self.journal.exists())
        self.assertEqual((self.old / "bin/personad").read_text(), "other-binary")

    def test_hardlinked_secret_is_refused(self):
        self.old_store()
        os.link(self.old / "secret.key", self.home / "linked-secret")
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertFalse(self.journal.exists())
        self.assertEqual(stat.S_IMODE((self.home / "linked-secret").stat().st_mode), 0o644)

    def test_no_clobber_rename_preserves_empty_destination_directory(self):
        source, target = self.home / "from", self.home / "to"
        source.mkdir()
        target.mkdir()
        with self.assertRaises(OSError):
            CUTOVER.rename_exclusive(source, target)
        self.assertTrue(source.exists() and target.exists())

    def test_unique_journal_and_home_lock_are_required(self):
        self.old_store()
        self.journal.mkdir(mode=0o700)
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertTrue(self.old.exists())
        self.journal.rmdir()
        lock = self.write(".persona-cutover.lock", "locked")
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertEqual(lock.read_text(), "locked")
        self.assertFalse(self.journal.exists())

    def test_journal_outside_home_or_inside_managed_roots_is_refused(self):
        self.old_store()
        for path in (self.old / ".persona-cutover-test", self.home / ".persona", self.home / ".." / "journal"):
            self.assertEqual(self.cli("--apply", "--approve", "--services-stopped", "--backup-verified",
                                      "--journal", path).returncode, 1 if ".." not in path.parts else 2)
            self.assertTrue(self.old.exists())

    def test_rollback_refuses_new_source_directory_and_replaced_binary(self):
        self.old_store()
        self.assertEqual(self.apply_cli().returncode, 0)
        binary = self.new / "bin/personad"
        binary.write_text("replacement-binary")
        self.assertEqual(self.rollback_cli().returncode, 1)
        self.assertEqual(binary.read_text(), "replacement-binary")
        self.assertTrue(self.new.exists())

    def test_rollback_refuses_occupied_original_root(self):
        self.old.mkdir(mode=0o700)
        self.assertEqual(self.apply_cli().returncode, 0)
        self.old.mkdir(mode=0o700)
        self.write(self.old.name + "/user-data", "new-data")
        self.assertEqual(self.rollback_cli().returncode, 1)
        self.assertEqual((self.old / "user-data").read_text(), "new-data")
        self.assertTrue(self.new.exists())

    def test_crash_after_rename_is_recoverable(self):
        self.old.mkdir(mode=0o700)
        original_save = CUTOVER.save
        def fail_after_move(journal, data):
            if data["operations"][0]["state"] == "done" and data["status"] == "applying":
                raise OSError("injected crash after rename")
            original_save(journal, data)
        with patch.object(CUTOVER, "save", side_effect=fail_after_move):
            with self.assertRaises(OSError):
                CUTOVER.apply(self.home, SOURCE, self.journal)
        self.assertTrue(self.new.exists())
        self.assertEqual(self.journal_data()["status"], "interrupted")
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue(self.old.exists())

    def test_crash_before_rename_is_recoverable(self):
        self.old.mkdir(mode=0o700)
        with patch.object(CUTOVER, "rename_exclusive", side_effect=OSError("injected failure")):
            with self.assertRaises(OSError):
                CUTOVER.apply(self.home, SOURCE, self.journal)
        self.assertEqual(self.journal_data()["operations"][0]["state"], "started")
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue(self.old.exists())

    def test_crash_after_alias_restoration_can_resume(self):
        self.write(".persona/hub.json", "{}")
        self.old.symlink_to(self.new.name, target_is_directory=True)
        self.assertEqual(self.apply_cli().returncode, 0)
        op = next(op for op in self.journal_data()["operations"] if op["action"] == "unlink_alias")
        CUTOVER.restore_one(self.home, op, self.journal)
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue(self.old.is_symlink())

    def test_journal_traversal_is_refused(self):
        self.old.mkdir(mode=0o700)
        self.assertEqual(self.apply_cli().returncode, 0)
        data = self.journal_data()
        data["operations"][0]["to"] = ".persona/../../outside"
        CUTOVER.save(self.journal, data)
        self.assertEqual(self.rollback_cli().returncode, 1)
        self.assertTrue(self.new.exists())

    def test_shell_configuration_is_never_executed(self):
        self.old_store()
        text = '$(touch "' + str(self.home / "EXECUTED") + '")\n'
        shell = self.write(".zshrc", text)
        unit = self.write(".config/systemd/user/" + SOURCE + "d.service", "Environment=" + text)
        self.assertEqual(self.apply_cli().returncode, 0)
        self.assertFalse((self.home / "EXECUTED").exists())
        self.assertEqual(shell.read_text(), text)
        self.assertEqual(unit.read_text(), "Environment=" + text)

    def test_typed_config_ids_change_but_messages_app_ids_and_tokens_do_not(self):
        self.old_store()
        obj = {"token": SOURCE, "notes": SOURCE, "app": {"bundle_id": "com.example." + SOURCE},
               "look": {"id": SOURCE}, "extensions": [{"id": SOURCE + "-apps", "config": {"message": SOURCE}}]}
        self.write(self.old.name + "/hub.json", json.dumps(obj), 0o640)
        self.assertEqual(self.apply_cli().returncode, 0)
        changed = json.loads((self.new / "hub.json").read_text())
        self.assertEqual(changed["look"]["id"], "persona-visor")
        self.assertEqual(changed["extensions"][0]["id"], "persona-apps")
        for field in ("token", "notes", "app"):
            self.assertEqual(changed[field], obj[field])
        self.assertEqual(changed["extensions"][0]["config"], obj["extensions"][0]["config"])
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(json.loads((self.old / "hub.json").read_text()), obj)
        self.assertEqual(stat.S_IMODE((self.old / "hub.json").stat().st_mode), 0o640)

    def test_avatar_package_directory_and_id_migrate_without_changing_mochi(self):
        self.write(self.old.name + "/personas/" + SOURCE + "/persona.json", json.dumps({"id": SOURCE, "name": SOURCE}))
        self.write(self.old.name + "/personas/persona/persona.json", '{"id":"persona"}')
        self.assertEqual(self.apply_cli().returncode, 0)
        changed = json.loads((self.new / "personas/persona-visor/persona.json").read_text())
        self.assertEqual(changed, {"id": "persona-visor", "name": SOURCE})
        self.assertEqual((self.new / "personas/persona/persona.json").read_text(), '{"id":"persona"}')
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue((self.old / "personas" / SOURCE / "persona.json").exists())

    def test_all_builtin_look_namespaces_migrate_only_typed_fields(self):
        for suffix in ("circuit", "pixel", "facet", "blob"):
            old_id = SOURCE + "-" + suffix
            new_id = "persona-" + suffix
            self.assertEqual(CUTOVER.look_id(old_id, SOURCE), new_id)
            self.assertEqual(CUTOVER.look_id(old_id + "/r1", SOURCE), new_id + "/r1")
            fixture = self.records_fixture()
            fixture["tables"]["settings"][0]["value"]["id"] = old_id
            fixture["tables"]["devices"][0]["prefs"]["look"]["package"] = old_id + "/r2"
            changed = CUTOVER.database_records(fixture, SOURCE)["tables"]
            self.assertEqual(changed["settings"][0]["value"]["id"], new_id)
            self.assertEqual(changed["devices"][0]["prefs"]["look"]["package"], new_id + "/r2")
            self.assertEqual(changed["messages"], fixture["tables"]["messages"])
            self.assertEqual(CUTOVER.config_ids({"look": {"id": old_id}, "message": old_id}, SOURCE),
                             {"look": {"id": new_id}, "message": old_id})
        self.assertEqual(CUTOVER.look_id("persona", SOURCE), "persona")
        self.write(self.old.name + "/personas/" + SOURCE + "-circuit/persona.json",
                   json.dumps({"id": SOURCE + "-circuit", "app_icon_fallback": SOURCE + "-pixel"}))
        self.assertEqual(self.apply_cli().returncode, 0)
        manifest = json.loads((self.new / "personas/persona-circuit/persona.json").read_text())
        self.assertEqual(manifest, {"id": "persona-circuit", "app_icon_fallback": "persona-pixel"})
        self.assertEqual(self.rollback_cli().returncode, 0)

    def test_avatar_and_extension_directory_conflicts_are_refused(self):
        self.write(self.old.name + "/personas/" + SOURCE + "/persona.json", '{"id":"previous"}')
        self.write(self.old.name + "/personas/persona-visor/persona.json", '{}')
        self.assertEqual(self.apply_cli().returncode, 1)
        self.assertFalse(self.journal.exists())

    def test_extension_data_directory_moves_without_rewriting_contents(self):
        self.write(self.old.name + "/extensions/data/" + SOURCE + "-apps/messages.json", SOURCE)
        self.assertEqual(self.apply_cli().returncode, 0)
        self.assertEqual((self.new / "extensions/data/persona-apps/messages.json").read_text(), SOURCE)
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertTrue((self.old / "extensions/data" / (SOURCE + "-apps") / "messages.json").exists())

    def test_explicit_harness_mcp_env_and_permissions_preserve_values_and_policy(self):
        fixture = {"mcpServers": {SOURCE: {"command": SOURCE + "d", "env": {"PREVIOUS_TOKEN": "sandbox-token", "OTHER": SOURCE}}},
                   "env": {"PREVIOUS_HOME": "unchanged-value", "TOKEN": SOURCE},
                   "permissions": {"allow": ["mcp__previous__read", "Bash(ls)"],
                                   "ask": ["mcp__previous__*"], "deny": ["mcp__previous__send"]},
                   "application_id": "com.example.previous", "message": SOURCE}
        harness = self.write(".agent/settings.json", json.dumps(fixture), 0o640)
        before = harness.read_bytes()
        self.assertEqual(self.cli("--managed-harness-json", harness).returncode, 0)
        self.assertEqual(harness.read_bytes(), before)
        result = self.apply_cli("--managed-harness-json", harness)
        self.assertEqual(result.returncode, 0, result.stderr)
        changed = json.loads(harness.read_text())
        self.assertNotIn(SOURCE, changed["mcpServers"])
        self.assertEqual(changed["mcpServers"]["persona"]["command"], SOURCE + "d")
        self.assertEqual(changed["mcpServers"]["persona"]["env"], {"PERSONA_TOKEN": "sandbox-token", "OTHER": SOURCE})
        self.assertEqual(changed["env"], {"PERSONA_HOME": "unchanged-value", "TOKEN": SOURCE})
        self.assertEqual(changed["permissions"], {"allow": ["mcp__persona__read", "Bash(ls)"],
                                                "ask": ["mcp__persona__*"], "deny": ["mcp__persona__send"]})
        self.assertEqual(changed["application_id"], fixture["application_id"])
        self.assertEqual(changed["message"], SOURCE)
        self.assertEqual(stat.S_IMODE(harness.stat().st_mode), 0o600)
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(harness.read_bytes(), before)
        self.assertEqual(stat.S_IMODE(harness.stat().st_mode), 0o640)

    def test_harness_tool_identifiers_change_but_arguments_and_other_servers_do_not(self):
        fixture = {"permissions": {
            "allow": ["mcp__previous__previous_settings(path=previous)", "mcp__other__ask_previous"],
            "ask": ["mcp__previous__ask_previous"],
            "deny": ["mcp__previous__previousd_state"],
        }}
        path = self.write(".agent/settings.json", json.dumps(fixture))
        self.assertEqual(self.apply_cli("--managed-harness-json", path).returncode, 0)
        self.assertEqual(json.loads(path.read_text())["permissions"], {
            "allow": ["mcp__persona__persona_settings(path=previous)", "mcp__other__ask_previous"],
            "ask": ["mcp__persona__ask_persona"], "deny": ["mcp__persona__personad_state"],
        })
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(json.loads(path.read_text()), fixture)

    def test_harness_key_conflicts_are_refused_not_merged(self):
        fixtures = [
            {"mcpServers": {SOURCE: {}, "persona": {}}},
            {"env": {"PREVIOUS_TOKEN": "one", "PERSONA_TOKEN": "two"}},
            {"permissions": {"allow": ["mcp__previous__read"], "deny": ["mcp__persona__read"]}},
            {"permissions": {"allow": ["mcp__previous__previous_settings"], "deny": ["mcp__persona__persona_settings"]}},
            {"permissions": {"allow": ["mcp__previous__previous_settings"], "deny": ["mcp__previous__persona_settings"]}},
        ]
        for fixture in fixtures:
            with self.subTest(fixture=fixture):
                path = self.write(".agent/settings.json", json.dumps(fixture))
                result = self.apply_cli("--managed-harness-json", path)
                self.assertEqual(result.returncode, 1)
                self.assertFalse(self.journal.exists())
                self.assertEqual(json.loads(path.read_text()), fixture)

    def test_harness_inside_moved_root_uses_projected_path(self):
        path = self.write(self.old.name + "/assistant/managed.json", '{"env":{"PREVIOUS_TOKEN":"unchanged"}}')
        self.assertEqual(self.apply_cli("--managed-harness-json", path).returncode, 0)
        output = self.new / "assistant/managed.json"
        self.assertEqual(json.loads(output.read_text()), {"env": {"PERSONA_TOKEN": "unchanged"}})
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(json.loads(path.read_text()), {"env": {"PREVIOUS_TOKEN": "unchanged"}})

    def test_unrecognized_harness_object_and_duplicate_paths_are_refused(self):
        path = self.write(".agent/messages.json", '{"message":"previous"}')
        self.assertEqual(self.apply_cli("--managed-harness-json", path).returncode, 1)
        self.assertEqual(self.cli("--managed-harness-json", path, "--managed-harness-json", path).returncode, 2)
        self.assertFalse(self.journal.exists())

    def records_fixture(self):
        return {"format": 1, "tables": {
            "plugins": [{"id": SOURCE + "-apps", "manifest": {"id": SOURCE + "-apps", "name": SOURCE,
              "contributes": {"looks": [{"id": "custom", "app_icon_fallback": SOURCE}]}}}],
            "plugin_secrets": [{"plugin": SOURCE + "-apps", "name": "token", "sealed": SOURCE}],
            "plugin_audit": [{"plugin": SOURCE + "-apps", "detail": {"text": SOURCE}}],
            "settings": [{"key": "look", "value": {"id": SOURCE}}, {"key": "custom", "value": SOURCE}],
            "hub_settings": [{"key": "look", "value": {"id": "persona"}}],
            "devices": [{"device_id": "demo", "topic": "com.example." + SOURCE, "relay_grant": "",
                         "prefs": {"look": {"package": SOURCE + "/r1", "design": "visor"}}}],
            "messages": [{"text": SOURCE}]}}

    def test_offline_database_records_exact_fields_and_rollback(self):
        fixture = self.records_fixture()
        records = self.write(".persona-cutover-records.json", json.dumps(fixture))
        self.assertEqual(self.apply_cli("--records", records).returncode, 0)
        result = json.loads(records.read_text())["tables"]
        self.assertEqual(result["plugins"][0]["id"], "persona-apps")
        self.assertEqual(result["plugin_secrets"][0], {"plugin": "persona-apps", "name": "token", "sealed": SOURCE})
        self.assertEqual(result["plugin_audit"][0]["detail"], {"text": SOURCE})
        self.assertEqual(result["settings"][0]["value"]["id"], "persona-visor")
        self.assertEqual(result["hub_settings"][0]["value"]["id"], "persona")
        self.assertEqual(result["devices"][0]["prefs"]["look"], {"package": "persona-visor/r1", "design": "visor"})
        self.assertEqual(result["messages"], fixture["tables"]["messages"])
        self.assertEqual(result["devices"][0]["topic"], "com.example." + SOURCE)
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(json.loads(records.read_text()), fixture)

    def test_offline_records_conflict_is_refused(self):
        fixture = self.records_fixture()
        fixture["tables"]["plugins"].append({"id": "persona-apps", "manifest": {}})
        with self.assertRaises(CUTOVER.Refusal):
            CUTOVER.database_records(fixture, SOURCE)

    def test_opaque_relay_grants_remain_unchanged_pending_runtime_renewal(self):
        fixture = self.records_fixture()
        fixture["tables"]["devices"][0]["relay_grant"] = "sandbox-opaque-grant"
        changed = CUTOVER.database_records(fixture, SOURCE)
        self.assertEqual(changed["tables"]["devices"][0]["relay_grant"], "sandbox-opaque-grant")
        self.assertEqual(changed["tables"]["devices"][0]["prefs"]["look"]["package"], "persona-visor/r1")
        self.write(self.old.name + "/hub.json", '{"relay_grant":"sandbox-opaque-grant"}')
        result = self.apply_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.new / "hub.json").read_text())["relay_grant"], "sandbox-opaque-grant")

    def projects(self):
        return self.home / ".claude" / "projects"

    def key(self, relative):
        return self.projects() / CUTOVER.project_key(self.home / relative)

    def rel(self, path):
        return str(path.relative_to(self.home))

    def tree(self, root):
        return {self.rel(path)[len(self.rel(root)) + 1:]: path.read_text() for path in sorted(root.rglob("*")) if path.is_file()}

    def test_claude_project_directories_move_with_the_root_and_roll_back(self):
        self.old_store()
        old_key, new_key = self.key(self.old.name + "/assistant"), self.key(".persona/assistant")
        self.write(self.rel(old_key) + "/s1.jsonl", '{"cwd": "assistant"}\n')
        self.write(self.rel(old_key) + "/memory/MEMORY.md", "notes")
        unrelated = self.write(self.rel(self.key("code/app")) + "/s9.jsonl", "elsewhere")
        root_key = self.write(self.rel(self.key(self.old.name)) + "/s0.jsonl", "root")
        before = CUTOVER.snapshot(old_key)
        result = self.cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn({"action": "move", "path": self.rel(old_key), "to": self.rel(new_key)},
                      json.loads(result.stdout)["operations"])
        self.assertTrue(old_key.is_dir())
        result = self.apply_cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(CUTOVER.present(old_key))
        self.assertEqual(CUTOVER.snapshot(new_key)["inode"], before["inode"])
        self.assertEqual(self.tree(new_key), {"memory/MEMORY.md": "notes", "s1.jsonl": '{"cwd": "assistant"}\n'})
        self.assertEqual((self.key(".persona") / "s0.jsonl").read_text(), "root")
        self.assertFalse(CUTOVER.present(root_key))
        self.assertEqual(unrelated.read_text(), "elsewhere")
        result = self.rollback_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(CUTOVER.snapshot(old_key), before)
        self.assertFalse(CUTOVER.present(new_key))
        self.assertEqual(root_key.read_text(), "root")

    def test_current_key_with_conversations_is_merged_not_copied(self):
        old_key, new_key = self.key(self.old.name + "/assistant"), self.key(".persona/assistant")
        old_files = {"s1.jsonl": "only before", "s2.jsonl": "copied", "memory/old.md": "o", "s1/subagents/a.jsonl": "sub"}
        new_files = {"s2.jsonl": "copied", "s3.jsonl": "only after", "memory/new.md": "n"}
        for name, text in old_files.items():
            self.write(self.rel(old_key) + "/" + name, text)
        for name, text in new_files.items():
            self.write(self.rel(new_key) + "/" + name, text)
        self.write(self.old.name + "/hub.json", "{}")
        result = self.apply_cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(CUTOVER.present(old_key))
        self.assertEqual(self.tree(new_key), {**old_files, **new_files})
        retired = [op for op in self.journal_data()["operations"] if op["action"] == "retire"]
        self.assertEqual([op["path"] for op in retired], [self.rel(old_key)])
        self.assertEqual(self.tree(self.journal / retired[0]["backup"]), {"s2.jsonl": "copied"})
        result = self.rollback_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.tree(old_key), old_files)
        self.assertEqual(self.tree(new_key), new_files)

    def test_conversation_file_differing_in_both_project_directories_is_refused(self):
        old_key, new_key = self.key(self.old.name + "/assistant"), self.key(".persona/assistant")
        self.write(self.rel(old_key) + "/s1.jsonl", "kept writing here")
        self.write(self.rel(new_key) + "/s1.jsonl", "and here")
        self.write(self.old.name + "/hub.json", "{}")
        result = self.apply_cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 1)
        self.assertIn("differs in the previous and current project directories", result.stderr)
        self.assertFalse(CUTOVER.present(self.journal))
        self.assertTrue(self.old.is_dir())
        self.assertEqual((old_key / "s1.jsonl").read_text(), "kept writing here")
        self.assertEqual((new_key / "s1.jsonl").read_text(), "and here")

    def test_project_alias_to_the_current_key_is_removed_and_restored(self):
        old_key, new_key = self.key(self.old.name + "/assistant"), self.key(".persona/assistant")
        self.write(self.rel(new_key) + "/s1.jsonl", "conversation")
        old_key.symlink_to(new_key.name)
        self.write(self.old.name + "/hub.json", "{}")
        result = self.apply_cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(CUTOVER.present(old_key))
        self.assertEqual((new_key / "s1.jsonl").read_text(), "conversation")
        self.assertEqual(self.rollback_cli().returncode, 0)
        self.assertEqual(os.readlink(old_key), new_key.name)

    def test_ambiguous_or_misplaced_project_directories_are_refused(self):
        self.write(self.old.name + "/hub.json", "{}")
        (self.home / ("." + SOURCE + "-tools")).mkdir()
        self.write(self.rel(self.key("." + SOURCE + "-tools/x")) + "/s1.jsonl", "sibling")
        result = self.cli("--claude-config-dir", self.home / ".claude")
        self.assertEqual(result.returncode, 1)
        self.assertIn("sibling of the previous root", result.stderr)
        result = self.cli("--claude-config-dir", self.home / ".missing")
        self.assertEqual(result.returncode, 1)
        self.assertIn("existing directory", result.stderr)
        (self.old / "claude").mkdir()
        result = self.cli("--claude-config-dir", self.old / "claude")
        self.assertEqual(result.returncode, 1)
        self.assertIn("inside a moved root", result.stderr)
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("agent conversation directories (--claude-config-dir)", json.loads(result.stdout)["manual"])

    def test_journal_project_directories_are_validated(self):
        self.write(self.rel(self.key(self.old.name + "/assistant")) + "/s1.jsonl", "conversation")
        self.write(self.old.name + "/hub.json", "{}")
        self.assertEqual(self.apply_cli("--claude-config-dir", self.home / ".claude").returncode, 0)
        data = self.journal_data()
        for bad in (["../outside/projects"], ["/abs/projects"], [".claude/other"], "projects"):
            changed = dict(data, claude_project_dirs=bad)
            with self.assertRaises(CUTOVER.Refusal):
                CUTOVER.validate_journal(self.home, SOURCE, self.journal, changed)
        moved = json.loads(json.dumps(data))
        moved["claude_project_dirs"] = []
        with self.assertRaises(CUTOVER.Refusal):
            CUTOVER.validate_journal(self.home, SOURCE, self.journal, moved)

    def test_database_sql_rewrites_stored_session_folders_of_listed_homes(self):
        result = self.cli("--print-database-sql", "--session-home", "/srv/other/")
        self.assertEqual(result.returncode, 0, result.stderr)
        for root in (str(self.home), "/srv/other"):
            self.assertIn("INSERT INTO cutover_roots VALUES ('" + root + "/." + SOURCE + "','" + root + "/.persona');",
                          result.stdout)
        for required in ("('node_sessions','cwd','text')", "\\if :cutover_node_sessions",
                         "LOCK TABLE public.node_sessions IN ACCESS EXCLUSIVE MODE;",
                         "Stored session folders remain under a previous root"):
            self.assertIn(required, result.stdout)
        for bad in ("relative/home", "/srv/it's", "/srv/a/../b", "/srv/back\\slash"):
            result = self.cli("--print-database-sql", "--session-home", bad)
            self.assertEqual(result.returncode, 1, bad)
            self.assertIn("session home", result.stderr)
        self.assertEqual(self.cli("--session-home", "/srv/other").returncode, 2)
        self.assertEqual(list(self.home.iterdir()), [])

    def test_database_sql_is_output_only_with_transaction_guards_and_verification(self):
        result = self.cli("--print-database-sql")
        self.assertEqual(result.returncode, 0, result.stderr)
        sql = result.stdout
        for required in ("\\set ON_ERROR_STOP on", "BEGIN;", "COMMIT;", "INSERT INTO cutover_name VALUES (:'source_name')",
                         "Cutover schema mismatch", "Extension ID conflict", "ACCESS EXCLUSIVE MODE",
                         "UPDATE public.plugin_secrets", "UPDATE public.plugin_audit", "Sealed secrets changed",
                         "{look,package}", "key='look'", "persona-visor", "remaining"):
            self.assertIn(required, sql)
        for table in ("settings", "hub_settings"):
            self.assertIn("LOCK TABLE public." + table + " IN ACCESS EXCLUSIVE MODE;", sql)
            self.assertIn("\\if :cutover_" + table, sql)
        self.assertNotIn("__TABLE_NAME__", sql)
        self.assertNotIn("LOCK settings", sql)
        self.assertNotIn("LOCK hub_settings", sql)
        self.assertNotIn("UPDATE public.messages", sql)
        self.assertNotIn("UPDATE public.device_tokens", sql)
        self.assertEqual(list(self.home.iterdir()), [])


@unittest.skipUnless(os.name != "nt" and all(shutil.which(name) for name in ("initdb", "pg_ctl", "psql")),
                     "isolated Postgres tools unavailable; SQL execution not verified")
class DatabaseSandboxTests(unittest.TestCase):
    """Own temporary cluster, Unix socket only. Never uses a configured database URL."""
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="persona-cutover-pg-")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name).resolve()
        cls.data = cls.root / "database"
        cls.run_program(["initdb", "-D", str(cls.data), "--auth=trust", "--username=postgres"], timeout=60)
        options = "-c listen_addresses='' -c unix_socket_directories='" + str(cls.root) + "'"
        cls.run_program(["pg_ctl", "-D", str(cls.data), "-l", str(cls.root / "postgres.log"),
                         "-o", options, "-w", "-t", "15", "start"], timeout=30)
        cls.addClassCleanup(cls.run_program, ["pg_ctl", "-D", str(cls.data), "-m", "fast", "-w", "-t", "15", "stop"])

    @classmethod
    def run_program(cls, args, input=None, timeout=30, check=True):
        environment = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
        environment.update(HOME=str(cls.root), XDG_CONFIG_HOME=str(cls.root / "config"),
                           XDG_DATA_HOME=str(cls.root / "data"), XDG_CACHE_HOME=str(cls.root / "cache"))
        result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=timeout,
                                cwd=str(SCRIPT.parent), env=environment)
        if check and result.returncode:
            raise AssertionError("sandbox command failed: " + result.stdout + result.stderr)
        return result

    def sql(self, text, check=True):
        # Every address argument targets the cluster created above, never PGHOST / PGSERVICE.
        return self.run_program(["psql", "-X", "--no-password", "-v", "ON_ERROR_STOP=1", "-h", str(self.root),
                                 "-p", "5432", "-U", "postgres", "-d", "postgres", "-At"], input=text, check=check)

    def setUp(self):
        self.sql("DROP SCHEMA public CASCADE; CREATE SCHEMA public; " + r"""
CREATE TABLE plugins (id text PRIMARY KEY, source text NOT NULL, origin text DEFAULT '', version text DEFAULT '1',
 previous_version text DEFAULT '', manifest jsonb DEFAULT '{}', granted jsonb DEFAULT '{}', granted_digest text DEFAULT '',
 enabled boolean DEFAULT true, config jsonb DEFAULT '{}', remote jsonb, disabled_contributions jsonb DEFAULT '[]',
 installed_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
CREATE TABLE plugin_secrets (plugin text REFERENCES plugins(id) ON DELETE CASCADE,name text,sealed text,PRIMARY KEY(plugin,name));
CREATE TABLE plugin_audit (id bigserial PRIMARY KEY,plugin text,detail jsonb DEFAULT '{}');
CREATE TABLE settings (key text PRIMARY KEY,value jsonb);
CREATE TABLE hub_settings (key text PRIMARY KEY,value jsonb);
CREATE TABLE devices (device_id text PRIMARY KEY,prefs jsonb,topic text,relay_grant text NOT NULL DEFAULT '');
CREATE TABLE node_sessions (machine_id text,session_id text,cwd text NOT NULL DEFAULT '',PRIMARY KEY(machine_id,session_id));
INSERT INTO plugins (id,source,manifest,config) VALUES
 ('previous-apps','installed','{"id":"previous-apps","name":"previous","contributes":{"looks":[{"id":"custom","app_icon_fallback":"previous"}]}}',
 '{"user_message":"previous"}');
INSERT INTO plugin_secrets VALUES ('previous-apps','token','sandbox-ciphertext');
INSERT INTO plugin_audit (plugin,detail) VALUES ('previous-apps','{"text":"previous"}');
INSERT INTO settings VALUES ('look','{"id":"previous","updated_at":"2026-01-01"}');
INSERT INTO hub_settings VALUES ('look','{"id":"persona"}'),('hub.extensions_report','{"cached_id":"previous-apps"}');
INSERT INTO devices (device_id,prefs,topic) VALUES ('fixture','{"look":{"package":"previous/r1","design":"visor"}}','com.example.previous');
""")

    def test_sql_migrates_only_typed_ids_preserves_secrets_and_can_run_again(self):
        self.sql(CUTOVER.database_sql(SOURCE))
        self.assertEqual(self.sql("SELECT id FROM plugins").stdout.strip(), "persona-apps")
        self.assertEqual(self.sql("SELECT plugin || ':' || sealed FROM plugin_secrets").stdout.strip(), "persona-apps:sandbox-ciphertext")
        self.assertEqual(self.sql("SELECT plugin || ':' || (detail->>'text') FROM plugin_audit").stdout.strip(), "persona-apps:previous")
        self.assertEqual(self.sql("SELECT manifest->>'id' FROM plugins").stdout.strip(), "persona-apps")
        self.assertEqual(self.sql("SELECT manifest#>>'{contributes,looks,0,app_icon_fallback}' FROM plugins").stdout.strip(), "persona-visor")
        self.assertEqual(self.sql("SELECT config->>'user_message' FROM plugins").stdout.strip(), "previous")
        self.assertEqual(self.sql("SELECT value->>'id' FROM settings WHERE key='look'").stdout.strip(), "persona-visor")
        self.assertEqual(self.sql("SELECT value->>'id' FROM hub_settings WHERE key='look'").stdout.strip(), "persona")
        self.assertEqual(self.sql("SELECT count(*) FROM hub_settings WHERE key='hub.extensions_report'").stdout.strip(), "0")
        self.assertEqual(self.sql("SELECT prefs#>>'{look,package}' FROM devices").stdout.strip(), "persona-visor/r1")
        self.assertEqual(self.sql("SELECT topic FROM devices").stdout.strip(), "com.example.previous")
        self.sql(CUTOVER.database_sql(SOURCE))
        self.assertEqual(self.sql("SELECT count(*) FROM plugins").stdout.strip(), "1")

    def test_sql_conflict_rolls_back_without_touching_any_rows(self):
        self.sql("INSERT INTO plugins (id,source) VALUES ('persona-apps','installed')")
        result = self.sql(CUTOVER.database_sql(SOURCE), check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Extension ID conflict", result.stderr)
        self.assertEqual(self.sql("SELECT plugin FROM plugin_secrets").stdout.strip(), "previous-apps")
        self.assertEqual(self.sql("SELECT value->>'id' FROM settings WHERE key='look'").stdout.strip(), "previous")

    def test_sql_schema_mismatch_rolls_back(self):
        self.sql("ALTER TABLE devices ALTER COLUMN prefs TYPE text USING prefs::text")
        result = self.sql(CUTOVER.database_sql(SOURCE), check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Cutover schema mismatch", result.stderr)
        self.assertEqual(self.sql("SELECT id FROM plugins").stdout.strip(), "previous-apps")

    def test_sql_maps_other_builtin_look_ids_and_retains_revision(self):
        self.sql("UPDATE settings SET value='{\"id\":\"previous-circuit\"}' WHERE key='look'; "
                 "UPDATE devices SET prefs='{\"look\":{\"package\":\"previous-facet/r2\",\"design\":\"facet\"}}'")
        self.sql(CUTOVER.database_sql(SOURCE))
        self.assertEqual(self.sql("SELECT value->>'id' FROM settings WHERE key='look'").stdout.strip(), "persona-circuit")
        self.assertEqual(self.sql("SELECT prefs#>>'{look,package}' FROM devices").stdout.strip(), "persona-facet/r2")
        self.assertEqual(self.sql("SELECT prefs#>>'{look,design}' FROM devices").stdout.strip(), "facet")
        self.assertEqual(self.sql("SELECT value->>'id' FROM hub_settings WHERE key='look'").stdout.strip(), "persona")

    def test_sql_preserves_opaque_relay_grant_for_later_renewal(self):
        self.sql("UPDATE devices SET relay_grant='sandbox-opaque-grant'")
        self.sql(CUTOVER.database_sql(SOURCE))
        self.assertEqual(self.sql("SELECT id FROM plugins").stdout.strip(), "persona-apps")
        self.assertEqual(self.sql("SELECT relay_grant FROM devices").stdout.strip(), "sandbox-opaque-grant")

    def test_sql_moves_stored_session_folders_under_listed_homes_only(self):
        self.sql("INSERT INTO node_sessions VALUES ('hub','a','/home/u/.previous/assistant'),('hub','b','/home/u/.previous'),"
                 "('hub','c','/home/u/.previous-tools/x'),('hub','d','/home/u/code/app'),('mac','e','/Users/me/.previous/health'),"
                 "('mac','f','/elsewhere/.previous/assistant'),('hub','g','')")
        expected = {"a": "/home/u/.persona/assistant", "b": "/home/u/.persona", "c": "/home/u/.previous-tools/x",
                    "d": "/home/u/code/app", "e": "/Users/me/.persona/health", "f": "/elsewhere/.previous/assistant", "g": ""}
        for _ in range(2):
            self.sql(CUTOVER.database_sql(SOURCE, ["/home/u", "/Users/me"]))
            rows = self.sql("SELECT session_id || '=' || cwd FROM node_sessions ORDER BY session_id").stdout.splitlines()
            self.assertEqual(dict(row.split("=", 1) for row in rows), expected)

    def test_sql_supports_hub_only_database(self):
        self.sql("DROP TABLE plugin_secrets,plugin_audit,plugins,settings,node_sessions")
        self.sql(CUTOVER.database_sql(SOURCE))
        self.assertEqual(self.sql("SELECT prefs#>>'{look,package}' FROM devices").stdout.strip(), "persona-visor/r1")


if __name__ == "__main__":
    unittest.main()
