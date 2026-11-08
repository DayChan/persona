#!/usr/bin/env python3
"""Offline one-shot cutover with typed ID edits. Never starts services or connects to a database."""

import argparse
from contextlib import contextmanager
import ctypes
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import uuid


class Refusal(Exception):
    """An unsafe or ambiguous operation; leave the filesystem alone."""


def present(path):
    return os.path.lexists(path)


def guard(home, path):
    """Require an owned path below HOME with no symlink ancestors."""
    try:
        relative = path.relative_to(home)
    except ValueError as exc:
        raise Refusal("path is outside the selected home") from exc
    if not relative.parts or ".." in relative.parts:
        raise Refusal("path must be strictly below the selected home")
    current = home
    for part in relative.parts[:-1]:
        current /= part
        if present(current) and (current.is_symlink() or not current.is_dir()):
            raise Refusal("symlink or non-directory ancestor: " + str(current.relative_to(home)))


def snapshot(path):
    if not present(path):
        return None
    info = path.lstat()
    result = {"device": info.st_dev, "inode": info.st_ino,
              "mode": stat.S_IMODE(info.st_mode), "kind": stat.S_IFMT(info.st_mode)}
    if stat.S_ISLNK(info.st_mode):
        result["target"] = os.readlink(path)
    elif stat.S_ISREG(info.st_mode):
        if info.st_nlink != 1:
            raise Refusal("hard-linked managed file requires manual migration")
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
        result["sha256"] = digest.hexdigest()
    elif not stat.S_ISDIR(info.st_mode):
        raise Refusal("managed path is not a regular file, directory or symlink")
    return result


def sync_dir(path):
    if os.name != "nt":
        fd = os.open(path, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def rename_exclusive(source, destination):
    """Atomic no-overwrite rename, including empty-directory conflicts."""
    if sys.platform.startswith("linux"):
        libc = ctypes.CDLL(None, use_errno=True)
        fn = getattr(libc, "renameat2", None)
        if fn is None:
            raise Refusal("atomic exclusive rename unavailable; migrate manually")
        fn.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
        fn.restype = ctypes.c_int
        rc = fn(-100, os.fsencode(source), -100, os.fsencode(destination), 1)
    elif sys.platform == "darwin":
        libc = ctypes.CDLL(None, use_errno=True)
        fn = getattr(libc, "renamex_np", None)
        if fn is None:
            raise Refusal("atomic exclusive rename unavailable; migrate manually")
        fn.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint]
        fn.restype = ctypes.c_int
        rc = fn(os.fsencode(source), os.fsencode(destination), 4)
    elif os.name == "nt":
        os.rename(source, destination)  # Windows refuses an existing destination.
        return
    else:
        raise Refusal("unsupported exclusive rename platform; migrate manually")
    if rc:
        raise OSError(ctypes.get_errno(), "exclusive rename failed")
    sync_dir(source.parent)
    if source.parent != destination.parent:
        sync_dir(destination.parent)


def extension_id(value, source_name):
    if isinstance(value, str):
        for old, new in ((source_name + "d-", "personad-"), (source_name + "-", "persona-")):
            if value.startswith(old):
                return new + value[len(old):]
    return value


def look_id(value, source_name):
    if value == source_name:
        return "persona-visor"
    if isinstance(value, str) and value.startswith(source_name + "/"):
        return "persona-visor" + value[len(source_name):]
    if isinstance(value, str) and value.startswith(source_name + "-"):
        return "persona" + value[len(source_name):]
    return value


def manifest_ids(value, source_name):
    result = copy.deepcopy(value)
    if not isinstance(result, dict):
        return result
    if "id" in result:
        result["id"] = extension_id(result["id"], source_name)
    looks = result.get("contributes", {}).get("looks", [])
    if isinstance(looks, list):
        for item in looks:
            if isinstance(item, dict):
                for field in ("id", "app_icon_fallback"):
                    if field in item:
                        item[field] = look_id(item[field], source_name)
    return result


def config_ids(value, source_name):
    """Only documented extension entries and appearance IDs; never general text."""
    result = copy.deepcopy(value)
    if not isinstance(result, dict):
        raise Refusal("managed JSON must be an object")
    for section in ("plugins", "extensions"):
        entries = result.get(section, [])
        if isinstance(entries, list):
            for item in entries:
                if isinstance(item, dict) and "id" in item:
                    item["id"] = extension_id(item["id"], source_name)
            ids = [item["id"] for item in entries if isinstance(item, dict) and "id" in item]
            if len(ids) != len(set(ids)):
                raise Refusal("extension ID conflict in managed config")
    if isinstance(result.get("look"), dict) and "id" in result["look"]:
        result["look"]["id"] = look_id(result["look"]["id"], source_name)
    return result


def database_records(value, source_name):
    """Pure rehearsal for an explicitly exported subset, not a database driver."""
    result = copy.deepcopy(value)
    if result.get("format") != 1 or not isinstance(result.get("tables"), dict):
        raise Refusal("database rehearsal requires format 1 and a tables object")
    tables = result["tables"]
    for table in ("plugins", "plugin_secrets", "plugin_audit"):
        for row in tables.get(table, []):
            field = "id" if table == "plugins" else "plugin"
            row[field] = extension_id(row[field], source_name)
            if table == "plugins":
                row["manifest"] = manifest_ids(row.get("manifest", {}), source_name)
    ids = [row["id"] for row in tables.get("plugins", [])]
    if len(ids) != len(set(ids)):
        raise Refusal("database extension ID conflict")
    for table in ("settings", "hub_settings"):
        for row in tables.get(table, []):
            if row.get("key") == "look" and isinstance(row.get("value"), dict):
                if "id" in row["value"]:
                    row["value"]["id"] = look_id(row["value"]["id"], source_name)
    for row in tables.get("devices", []):
        prefs = row.get("prefs") or {}
        look = prefs.get("look") or {}
        if "package" in look:
            look["package"] = look_id(look["package"], source_name)
    return result


def harness_ids(value, source_name):
    """Rename namespaces in an explicitly approved harness object, not its values."""
    result = copy.deepcopy(value)
    if not isinstance(result, dict) or not any(key in result for key in ("mcpServers", "permissions", "env")):
        raise Refusal("explicit harness JSON has no supported configuration fields")

    def environment(obj):
        if not isinstance(obj, dict):
            raise Refusal("harness env must be an object")
        prefix = source_name.upper().replace("-", "_") + "_"
        for key in list(obj):
            if key.startswith(prefix):
                target = "PERSONA_" + key[len(prefix):]
                if target in obj:
                    raise Refusal("old/new environment key conflict")
                obj[target] = obj.pop(key)

    if "env" in result:
        environment(result["env"])
    if "mcpServers" in result:
        servers = result["mcpServers"]
        if not isinstance(servers, dict):
            raise Refusal("harness mcpServers must be an object")
        if source_name in servers:
            if "persona" in servers:
                raise Refusal("old/new MCP server key conflict")
            servers["persona"] = servers.pop(source_name)
        if isinstance(servers.get("persona"), dict) and "env" in servers["persona"]:
            environment(servers["persona"]["env"])
    if "permissions" in result:
        permissions = result["permissions"]
        if not isinstance(permissions, dict):
            raise Refusal("harness permissions must be an object")
        rules = []
        for category in ("allow", "ask", "deny"):
            if category in permissions:
                if not isinstance(permissions[category], list) or not all(isinstance(rule, str) for rule in permissions[category]):
                    raise Refusal("permission lists must contain strings")
                rules.extend(permissions[category])
        old = "mcp__" + source_name + "__"
        aliases = {source_name: "persona", source_name + "d": "personad"}
        mapped_rules = {}
        for category in ("allow", "ask", "deny"):
            if category not in permissions:
                continue
            mapped = []
            for rule in permissions[category]:
                target = rule
                if rule.startswith(old):
                    tool, separator, arguments = rule[len(old):].partition("(")
                    tool = "_".join(aliases.get(part, part) for part in tool.split("_"))
                    target = "mcp__persona__" + tool + separator + arguments
                if target != rule and target in rules:
                    raise Refusal("old/new permission rule conflict; no merge or widening")
                if target in mapped_rules and mapped_rules[target] != rule:
                    raise Refusal("permission identifiers collapse; review the original policies")
                mapped_rules[target] = rule
                mapped.append(target)
            permissions[category] = mapped
    return result


def json_operation(home, disk_path, output_path, transform, source_name, mode=None):
    guard(home, disk_path)
    before = snapshot(disk_path)
    if before is None:
        return None
    if before["kind"] != stat.S_IFREG:
        raise Refusal("managed JSON must be a private regular file")
    with disk_path.open(encoding="utf-8") as stream:
        original = json.load(stream)
    changed = transform(original, source_name)
    if original == changed:
        return None
    if mode is not None:
        before = dict(before, mode=mode)
    return {"action": "write_json", "path": str(output_path.relative_to(home)),
            "before": before, "text": json.dumps(changed, indent=2, ensure_ascii=False) + "\n",
            "state": "pending"}


# Claude Code shortens a longer project directory name and adds a hash this tool does not reproduce.
PROJECT_KEY_LIMIT = 200


def project_key(path):
    """Claude Code's directory name for a folder's conversations: every character but ASCII letters and digits is '-'."""
    return re.sub(r"[^A-Za-z0-9]", "-", str(path))


def project_operations(home, source_name, config):
    """Move the conversation directories of folders under the previous root to the keys of the same folders under
    .persona. Where the current key already has conversations, merge: move what it lacks, then retire the rest of
    the previous directory (byte-identical copies only) into the journal. A file that differs in the two is refused;
    the tool never picks one."""
    projects = config / "projects"
    guard(home, projects / "placeholder")
    if not projects.is_dir() or projects.is_symlink():
        return []
    previous, current = project_key(home / ("." + source_name)), project_key(home / ".persona")
    # A sibling such as ~/.<previous-name>-tools has the same key as the folder "tools" inside the root.
    siblings = [project_key(home / entry.name) for entry in home.iterdir()
                if entry.name.startswith("." + source_name) and entry.name != "." + source_name]
    operations = []

    def operation(action, path, **extra):
        operations.append({"action": action, "path": str(path.relative_to(home)),
                           "before": snapshot(path), "state": "pending", **extra})

    def merge(source, target):
        for child in sorted(source.iterdir()):
            there = target / child.name
            if not present(there):
                operation("move", child, to=str(there.relative_to(home)))
            elif (child.is_dir() and there.is_dir()) and not (child.is_symlink() or there.is_symlink()):
                merge(child, there)
            else:
                mine, theirs = snapshot(child), snapshot(there)
                if mine["kind"] != stat.S_IFREG or theirs["kind"] != stat.S_IFREG or mine["sha256"] != theirs["sha256"]:
                    raise Refusal("a conversation file differs in the previous and current project directories; "
                                  "merge it by hand: " + str(there.relative_to(home)))

    for entry in sorted(projects.iterdir()):
        name = entry.name
        if name != previous and not name.startswith(previous + "-"):
            continue
        if any(name == key or name.startswith(key + "-") for key in siblings):
            raise Refusal("project directory may belong to a sibling of the previous root; migrate it by hand: "
                          + str(entry.relative_to(home)))
        target = projects / (current + name[len(previous):])
        if len(name) > PROJECT_KEY_LIMIT or len(target.name) > PROJECT_KEY_LIMIT:
            raise Refusal("Claude Code shortens long project directory names; migrate it by hand: " + str(entry.relative_to(home)))
        if entry.is_symlink():
            if target.is_symlink() or not target.is_dir() or Path(os.path.realpath(entry)) != target:
                raise Refusal("project directory alias is not the current key's directory")
            operation("unlink_alias", entry)
        elif not entry.is_dir():
            raise Refusal("project entry is not a directory: " + str(entry.relative_to(home)))
        elif not present(target):
            operation("move", entry, to=str(target.relative_to(home)))
        elif target.is_symlink():
            if Path(os.path.realpath(target)) != entry:
                raise Refusal("current project directory is an alias to somewhere else")
            operation("unlink_alias", target)
            operation("move", entry, to=str(target.relative_to(home)))
        elif target.is_dir():
            merge(entry, target)
            operation("retire", entry)
        else:
            raise Refusal("current project entry is not a directory: " + str(target.relative_to(home)))
    return operations


def plan(home, source_name, records=None, harnesses=(), claude_dirs=()):
    operations = []
    roots = []

    def operation(action, path, **extra):
        operations.append({"action": action, "path": str(path.relative_to(home)),
                           "before": snapshot(path), "state": "pending", **extra})

    for parent, old_name, new_name in ((home, "." + source_name, ".persona"),
                                        (home / ".config", source_name, "persona")):
        old, new = parent / old_name, parent / new_name
        guard(home, old)
        guard(home, new)
        if not present(old) and not present(new):
            continue  # Do not invent an empty store on an unconfigured machine.
        old_real = present(old) and not old.is_symlink() and old.is_dir()
        new_real = present(new) and not new.is_symlink() and new.is_dir()
        if old_real and not present(new):
            operation("move", old, to=str(new.relative_to(home)))
            roots.append((new, old))
        elif old_real and new.is_symlink():
            if new.resolve(strict=True) != old:
                raise Refusal("destination alias is not the source directory")
            operation("unlink_alias", new)
            operation("move", old, to=str(new.relative_to(home)))
            roots.append((new, old))
        elif new_real and old.is_symlink():
            if old.resolve(strict=True) != new:
                raise Refusal("source alias is not the destination directory")
            operation("unlink_alias", old)
            roots.append((new, new))
        elif new_real and not present(old):
            roots.append((new, new))
        else:
            raise Refusal("independent stores, invalid paths or external aliases require manual resolution")

    for new_root, disk_root in roots:
        root_info = snapshot(disk_root)
        if root_info["mode"] != 0o700:
            operations.append({"action": "chmod", "path": str(new_root.relative_to(home)),
                               "before": root_info, "mode": 0o700, "state": "pending"})
        if new_root.name == ".persona":
            binary_names = (("d", "personad"), ("-link", "persona-link"), ("d-wechat", "personad-wechat"))
            for suffix, target in ((old + ext, new + ext) for old, new in binary_names for ext in ("", ".exe")):
                old_bin, new_bin = disk_root / "bin" / (source_name + suffix), disk_root / "bin" / target
                guard(home, old_bin)
                guard(home, new_bin)
                if present(old_bin) and present(new_bin):
                    raise Refusal("both binary names exist; do not overwrite either")
                if present(old_bin):
                    before = snapshot(old_bin)
                    if before["kind"] != stat.S_IFREG:
                        raise Refusal("binary aliases require manual migration")
                    operations.append({"action": "move", "path": str((new_root / "bin" / old_bin.name).relative_to(home)),
                                       "to": str((new_root / "bin" / target).relative_to(home)),
                                       "before": before, "backup_binary": True, "state": "pending"})
            secrets = [disk_root / item for item in (
                "hub.json", "manager.json", "link.json", "relay.json", "relay.secret", "tailcat.json", "tailcat-hub.json", "apns.json",
                "machine-uid", "secret.key", "handles.key", "apns_key.p8",
                "tls/hub.key", "tls/hub.crt", "link-tls/link.key", "link-tls/link.crt")]
            managers = disk_root / "managers"
            guard(home, managers / "placeholder")
            if managers.is_dir():
                for manager in sorted(managers.iterdir()):
                    guard(home, manager / "placeholder")
                    if manager.is_dir():
                        secrets.extend(manager / name for name in ("manager.json", "secret.key", "handles.key"))
            launchers = disk_root / "launcher-settings"
            guard(home, launchers / "placeholder")
            if launchers.is_dir():
                secrets.extend(sorted(launchers.glob("*.json")))
        else:
            secrets = [disk_root / "config.json"]
        for path in secrets:
            guard(home, path)
            if not present(path):
                continue
            before = snapshot(path)
            if before["kind"] != stat.S_IFREG:
                raise Refusal("credential path requires a private regular file")
            if before["mode"] != 0o600:
                operations.append({"action": "chmod", "path": str((new_root / path.relative_to(disk_root)).relative_to(home)),
                                   "before": before, "mode": 0o600, "state": "pending"})
            if path.name in ("hub.json", "manager.json", "config.json"):
                edit = json_operation(home, path, new_root / path.relative_to(disk_root), config_ids, source_name, 0o600)
                if edit:
                    operations.append(edit)
        if new_root.name == ".persona":
            packages = disk_root / "personas"
            guard(home, packages / "placeholder")
            if packages.is_dir():
                for package in sorted(packages.iterdir()):
                    guard(home, package / "placeholder")
                    if not package.is_dir():
                        continue
                    target = new_root / "personas" / look_id(package.name, source_name)
                    if target.name != package.name:
                        if present(packages / target.name):
                            raise Refusal("avatar directory ID conflict")
                        operations.append({"action": "move", "path": str((new_root / "personas" / package.name).relative_to(home)),
                                           "to": str(target.relative_to(home)), "before": snapshot(package), "state": "pending"})
                    def package_ids(data, source):
                        result = copy.deepcopy(data)
                        if "id" in result:
                            result["id"] = look_id(result["id"], source)
                        if "app_icon_fallback" in result:
                            result["app_icon_fallback"] = look_id(result["app_icon_fallback"], source)
                        return result
                    edit = json_operation(home, package / "persona.json", target / "persona.json", package_ids, source_name)
                    if edit:
                        operations.append(edit)
            for area in ("installed", "data", "tools"):
                directory = disk_root / "extensions" / area
                guard(home, directory / "placeholder")
                if directory.is_dir():
                    for entry in sorted(directory.iterdir()):
                        renamed = extension_id(entry.name, source_name)
                        if renamed == entry.name:
                            continue
                        if entry.is_symlink() or not entry.is_dir() or present(directory / renamed):
                            raise Refusal("extension directory ID conflict or alias")
                        operations.append({"action": "move", "path": str((new_root / "extensions" / area / entry.name).relative_to(home)),
                                           "to": str((new_root / "extensions" / area / renamed).relative_to(home)),
                                           "before": snapshot(entry), "state": "pending"})
    for path in harnesses:
        guard(home, path)
        if path.suffix != ".json" or not present(path):
            raise Refusal("explicit managed harness must be an existing JSON file below the selected home")
        output = path
        for new_root, disk_root in roots:
            if path.is_relative_to(disk_root):
                output = new_root / path.relative_to(disk_root)
                break
        if any(op["path"] == str(output.relative_to(home)) and op["action"] in ("chmod", "write_json") for op in operations):
            raise Refusal("explicit harness overlaps another edit; migrate it in a separate stopped-services phase")
        edit = json_operation(home, path, output, harness_ids, source_name)
        if edit:
            edit["managed_harness"] = True
            operations.append(edit)
    managed = [home / ("." + source_name), home / ".persona", home / ".config" / source_name, home / ".config" / "persona"]
    for config in claude_dirs:
        guard(home, config)
        if not config.is_dir() or config.is_symlink():
            raise Refusal("explicit Claude Code config directory must be an existing directory below the selected home")
        if any(config == root or config.is_relative_to(root) for root in managed):
            raise Refusal("Claude Code config directory inside a moved root; migrate it separately")
        operations.extend(project_operations(home, source_name, config))
    if records:
        if records != home / ".persona-cutover-records.json":
            raise Refusal("rehearsal input must be .persona-cutover-records.json directly below the selected home")
        edit = json_operation(home, records, records, database_records, source_name)
        if edit:
            operations.append(edit)
    return operations


def save(journal, data):
    temp = journal / (".journal-" + uuid.uuid4().hex)
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(data, stream, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, journal / "journal.json")
        sync_dir(journal)
    finally:
        if present(temp):
            temp.unlink()


def check(home, relative):
    path = home / relative
    guard(home, path)
    return path


def expect(path, expected):
    if snapshot(path) != expected:
        raise Refusal("managed path changed since planning; stop and investigate")


def validate_journal_path(home, journal):
    guard(home, journal)
    if journal.parent != home or not re.fullmatch(r"\.persona-cutover-[a-zA-Z0-9_-]+", journal.name):
        raise Refusal("journal must be a new .persona-cutover-<id> directory directly below the selected home")


@contextmanager
def transaction_lock(home):
    lock = home / ".persona-cutover.lock"
    fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
    sync_dir(home)
    try:
        yield
    finally:
        lock.unlink()
        sync_dir(home)


def copy_private(source, destination):
    fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with source.open("rb") as src, os.fdopen(fd, "wb") as dst:
        shutil.copyfileobj(src, dst)
        dst.flush()
        os.fsync(dst.fileno())


def replace_bytes(path, contents, expected, mode):
    temp = path.parent / (".persona-cutover-write-" + uuid.uuid4().hex)
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        expect(path, expected)
        os.chmod(temp, mode)
        os.replace(temp, path)
        sync_dir(path.parent)
    finally:
        if present(temp):
            temp.unlink()


def content_matches(current, expected):
    return current is not None and all(current.get(key) == expected.get(key)
                                       for key in ("kind", "mode", "sha256", "target"))


def apply(home, source_name, journal, records=None, harnesses=(), claude_dirs=()):
    operations = plan(home, source_name, records, harnesses, claude_dirs)  # Preflight every operation before any mutation.
    validate_journal_path(home, journal)
    journal.mkdir(mode=0o700)  # Exclusive creation: never reuse another transaction.
    os.chmod(journal, 0o700)
    sync_dir(home)
    data = {"version": 1, "home": str(home), "source_name": source_name,
            "managed_harness_paths": [op["path"] for op in operations if op.get("managed_harness")],
            "claude_project_dirs": [str((config / "projects").relative_to(home)) for config in claude_dirs],
            "status": "applying", "operations": operations}
    save(journal, data)
    try:
        for index, op in enumerate(operations):
            path = check(home, op["path"])
            expect(path, op["before"])
            if op["action"] == "move":
                destination = check(home, op["to"])
                if present(destination):
                    raise Refusal("destination exists; refusing to overwrite")
                if op.get("backup_binary"):
                    backup = journal / ("binary-" + str(index))
                    copy_private(path, backup)
                    expect(path, op["before"])
                    if snapshot(backup)["sha256"] != op["before"]["sha256"]:
                        raise Refusal("binary backup failed verification")
                    op["backup"] = backup.name
            elif op["action"] == "write_json":
                backup = journal / ("json-" + str(index))
                copy_private(path, backup)
                if snapshot(backup)["sha256"] != op["before"]["sha256"]:
                    raise Refusal("JSON backup failed verification")
                op["backup"] = backup.name
                op["after"] = {"kind": stat.S_IFREG, "mode": 0o600,
                               "sha256": hashlib.sha256(op["text"].encode("utf-8")).hexdigest()}
            elif op["action"] == "retire":
                retired = journal / ("retired-" + str(index))
                if present(retired):
                    raise Refusal("retired path exists in the journal; refusing to overwrite")
                op["backup"] = retired.name
            op["state"] = "started"
            save(journal, data)  # Intent reaches disk before the filesystem changes.
            if op["action"] == "move":
                rename_exclusive(path, destination)
            elif op["action"] == "unlink_alias":
                path.unlink()
                sync_dir(path.parent)
            elif op["action"] == "retire":
                rename_exclusive(path, journal / op["backup"])
            elif op["action"] == "write_json":
                replace_bytes(path, op["text"].encode("utf-8"), op["before"], 0o600)
            else:
                path.chmod(op["mode"])
                sync_dir(path)
            op["state"] = "done"
            save(journal, data)
        data["status"] = "applied"
        save(journal, data)
    except BaseException:
        data["status"] = "interrupted"
        save(journal, data)
        raise
    return len(operations)


def validate_journal(home, source_name, journal, data):
    """Treat journals as data, not as instructions with unrestricted paths."""
    if (data.get("version") != 1 or data.get("home") != str(home)
            or data.get("source_name") != source_name
            or data.get("status") not in ("applying", "interrupted", "applied", "rolling_back", "rolled_back")):
        raise Refusal("journal identity or version mismatch")
    operations = data.get("operations")
    if not isinstance(operations, list):
        raise Refusal("invalid journal operations")
    roots = ("." + source_name, ".persona", ".config/" + source_name, ".config/persona", ".persona-cutover-records.json")
    projects = data.get("claude_project_dirs", [])
    if not isinstance(projects, list) or not all(
            isinstance(item, str) and item and not Path(item).is_absolute() and ".." not in Path(item).parts
            and Path(item).name == "projects" for item in projects):
        raise Refusal("invalid journal project directories")
    for op in operations:
        if (not isinstance(op, dict) or op.get("action") not in ("move", "unlink_alias", "chmod", "write_json", "retire")
                or op.get("state") not in ("pending", "started", "done", "undo_started", "undone")):
            raise Refusal("invalid journal operation")
        for field in ("path", "to") if op["action"] == "move" else ("path",):
            relative = op.get(field)
            allowed_harness = op["action"] == "write_json" and relative in data.get("managed_harness_paths", [])
            allowed_project = (op["action"] in ("move", "unlink_alias", "retire") and isinstance(relative, str)
                               and any(relative.startswith(item + "/") for item in projects))
            in_roots = isinstance(relative, str) and any(relative == root or relative.startswith(root + "/") for root in roots)
            if not isinstance(relative, str) or not (allowed_harness or allowed_project or (in_roots and op["action"] != "retire")):
                raise Refusal("journal path is outside managed roots")
            lexical = Path(relative)
            if lexical.is_absolute() or ".." in lexical.parts:
                raise Refusal("journal path traversal")
        if not isinstance(op.get("before"), dict):
            raise Refusal("journal has no original identity")
        if op["action"] == "chmod" and op.get("mode") not in (0o600, 0o700):
            raise Refusal("invalid journal permissions")
        if op.get("backup") and not re.fullmatch(r"(?:binary|json|retired)-[0-9]+", op["backup"]):
            raise Refusal("invalid backup name")
        if op["action"] == "retire" and not op.get("backup") and op["state"] != "pending":
            raise Refusal("retired directory has no journal entry")
    validate_journal_path(home, journal)


def restore_one(home, op, journal):
    path = check(home, op["path"])
    before = op["before"]
    if op["action"] == "move":
        destination = check(home, op["to"])
        if snapshot(path) == before and not present(destination):
            return  # Intent was saved, but the move did not happen (or was undone).
        expect(destination, before)
        if present(path):
            raise Refusal("rollback source exists; refusing to overwrite")
        rename_exclusive(destination, path)
    elif op["action"] == "retire":
        retired = journal / op["backup"]
        if snapshot(path) == before and not present(retired):
            return  # Intent was saved, but the directory never left (or is back).
        expect(retired, before)
        if present(path):
            raise Refusal("rollback source exists; refusing to overwrite")
        rename_exclusive(retired, path)
    elif op["action"] == "unlink_alias":
        if content_matches(snapshot(path), before):
            return
        if present(path):
            raise Refusal("rollback alias path occupied; refusing to overwrite")
        os.symlink(before["target"], path)
        sync_dir(path.parent)
    elif op["action"] == "write_json":
        now = snapshot(path)
        if content_matches(now, before):
            return
        if not content_matches(now, op["after"]):
            raise Refusal("rollback JSON changed; refusing to overwrite")
        backup = journal / op["backup"]
        if backup.is_symlink() or not content_matches(snapshot(backup), dict(before, mode=0o600)):
            raise Refusal("JSON backup identity mismatch")
        replace_bytes(path, backup.read_bytes(), now, before["mode"])
    else:
        now = snapshot(path)
        after = dict(before, mode=op["mode"])
        if content_matches(now, before):
            return
        if not content_matches(now, after):
            raise Refusal("rollback file changed; refusing to restore its permissions")
        path.chmod(before["mode"])
        sync_dir(path)


def rollback(home, source_name, journal):
    guard(home, journal / "journal.json")
    if journal.is_symlink() or stat.S_IMODE(journal.stat().st_mode) != 0o700:
        raise Refusal("journal directory must be private (0700)")
    record = journal / "journal.json"
    if record.is_symlink() or stat.S_IMODE(record.stat().st_mode) != 0o600:
        raise Refusal("journal file must be private (0600)")
    with record.open(encoding="utf-8") as stream:
        data = json.load(stream)
    validate_journal(home, source_name, journal, data)
    # A lock is intentionally persistent after an unclean exit. Inspect it before
    # manually removing it; never allow two recovery processes to race.
    lock = journal / "rollback.lock"
    fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
    try:
        data["status"] = "rolling_back"
        save(journal, data)
        for op in reversed(data["operations"]):
            if op["state"] in ("pending", "undone"):
                continue
            op["state"] = "undo_started"
            save(journal, data)
            restore_one(home, op, journal)
            op["state"] = "undone"
            save(journal, data)
        data["status"] = "rolled_back"
        save(journal, data)
    finally:
        lock.unlink()
        sync_dir(journal)
    return len(data["operations"])


def session_roots(source_name, homes):
    """Previous and current data roots for the homes whose stored sessions share a database (POSIX paths only)."""
    roots = []
    for home in homes:
        text = str(home)
        if len(text) > 1:
            text = text.rstrip("/")
        if not re.fullmatch(r"/[^\x00-\x1f\x7f'\\]+", text) or "/../" in text + "/" or "/./" in text + "/":
            raise Refusal("session home must be an absolute POSIX path without quotes, backslashes or traversal; "
                          "rewrite other stored session folders by hand")
        pair = (text + "/." + source_name, text + "/.persona")
        if pair not in roots:
            roots.append(pair)
    return roots


def database_sql(source_name, homes=()):
    """Emit a reviewed psql transaction; this program never connects to Postgres."""
    header = "\\set ON_ERROR_STOP on\n\\set source_name " + source_name + "\n"
    roots = "".join("INSERT INTO cutover_roots VALUES ('" + previous + "','" + current + "');\n"
                    for previous, current in session_roots(source_name, homes))
    return (header + r"""BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '120s';
SET LOCAL search_path = pg_temp, public;
CREATE TEMP TABLE cutover_name (source text NOT NULL);
INSERT INTO cutover_name VALUES (:'source_name');
CREATE TEMP TABLE cutover_roots (previous text NOT NULL, current text NOT NULL);
__SESSION_ROOTS__SELECT to_regclass('public.plugins') IS NOT NULL AS registry,
       to_regclass('public.settings') IS NOT NULL AS settings,
       to_regclass('public.hub_settings') IS NOT NULL AS hub_settings,
       to_regclass('public.devices') IS NOT NULL AS devices,
       to_regclass('public.node_sessions') IS NOT NULL AS node_sessions
\gset cutover_
CREATE TEMP TABLE cutover_columns (table_name text, column_name text, data_type text);
INSERT INTO cutover_columns VALUES
 ('plugins','id','text'), ('plugins','source','text'), ('plugins','origin','text'),
 ('plugins','version','text'), ('plugins','previous_version','text'), ('plugins','manifest','jsonb'),
 ('plugins','granted','jsonb'), ('plugins','granted_digest','text'), ('plugins','enabled','boolean'),
 ('plugins','config','jsonb'), ('plugins','remote','jsonb'), ('plugins','disabled_contributions','jsonb'),
 ('plugins','installed_at','timestamp with time zone'), ('plugins','updated_at','timestamp with time zone'),
 ('plugin_secrets','plugin','text'), ('plugin_secrets','name','text'), ('plugin_secrets','sealed','text'),
 ('plugin_audit','plugin','text'), ('settings','key','text'), ('settings','value','jsonb'),
 ('hub_settings','key','text'), ('hub_settings','value','jsonb'), ('devices','prefs','jsonb'),
 ('node_sessions','cwd','text');
DO $check$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM cutover_columns WHERE to_regclass('public.' || table_name) IS NOT NULL) THEN
   RAISE EXCEPTION 'No supported cutover tables found';
 END IF;
 IF EXISTS (SELECT 1 FROM cutover_columns e WHERE to_regclass('public.' || e.table_name) IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM information_schema.columns c WHERE c.table_schema='public'
     AND c.table_name=e.table_name AND c.column_name=e.column_name AND c.data_type=e.data_type)) THEN
   RAISE EXCEPTION 'Cutover schema mismatch; stop and review the frozen schema';
 END IF;
 IF (to_regclass('public.plugins') IS NOT NULL) IS DISTINCT FROM (to_regclass('public.plugin_secrets') IS NOT NULL)
    OR (to_regclass('public.plugins') IS NOT NULL) IS DISTINCT FROM (to_regclass('public.plugin_audit') IS NOT NULL) THEN
   RAISE EXCEPTION 'Incomplete extension registry schema';
 END IF;
END $check$;
CREATE FUNCTION pg_temp.cutover_extension_id(v text) RETURNS text LANGUAGE sql AS $fn$
 SELECT CASE WHEN left(v,length(source)+2)=source || 'd-' THEN 'personad-' || substr(v,length(source)+3)
             WHEN left(v,length(source)+1)=source || '-' THEN 'persona-' || substr(v,length(source)+2)
             ELSE v END FROM cutover_name
$fn$;
CREATE FUNCTION pg_temp.cutover_look_id(v text) RETURNS text LANGUAGE sql AS $fn$
 SELECT CASE WHEN v=source THEN 'persona-visor'
             WHEN left(v,length(source)+1)=source || '/' THEN 'persona-visor' || substr(v,length(source)+1)
             WHEN left(v,length(source)+1)=source || '-' THEN 'persona' || substr(v,length(source)+1)
             ELSE v END FROM cutover_name
$fn$;
CREATE FUNCTION pg_temp.cutover_manifest(v jsonb) RETURNS jsonb LANGUAGE plpgsql AS $fn$
DECLARE r jsonb := v; items jsonb; item jsonb; f text;
BEGIN
 IF jsonb_typeof(r->'id')='string' THEN
   r := jsonb_set(r,'{id}',to_jsonb(pg_temp.cutover_extension_id(r->>'id')),false);
 END IF;
 IF jsonb_typeof(r#>'{contributes,looks}')='array' THEN
   items := '[]'::jsonb;
   FOR item IN SELECT value FROM jsonb_array_elements(r#>'{contributes,looks}') LOOP
     FOREACH f IN ARRAY ARRAY['id','app_icon_fallback'] LOOP
       IF jsonb_typeof(item->f)='string' THEN
         item := jsonb_set(item,ARRAY[f],to_jsonb(pg_temp.cutover_look_id(item->>f)),false);
       END IF;
     END LOOP;
     items := items || jsonb_build_array(item);
   END LOOP;
   r := jsonb_set(r,'{contributes,looks}',items,false);
 END IF;
 RETURN r;
END $fn$;
\if :cutover_registry
LOCK TABLE public.plugins, public.plugin_secrets, public.plugin_audit IN ACCESS EXCLUSIVE MODE;
DO $check$
BEGIN
 IF EXISTS (SELECT 1 FROM public.plugins p JOIN public.plugins q
   ON q.id=pg_temp.cutover_extension_id(p.id) WHERE p.id<>q.id) THEN
   RAISE EXCEPTION 'Extension ID conflict; no merge or overwrite allowed';
 END IF;
END $check$;
CREATE TEMP TABLE cutover_secrets AS
 SELECT pg_temp.cutover_extension_id(plugin) AS plugin,name,sealed FROM public.plugin_secrets;
CREATE TEMP TABLE cutover_ids AS SELECT id,pg_temp.cutover_extension_id(id) AS target FROM public.plugins
 WHERE id<>pg_temp.cutover_extension_id(id);
INSERT INTO public.plugins (id,source,origin,version,previous_version,manifest,granted,granted_digest,enabled,
 config,remote,disabled_contributions,installed_at,updated_at)
 SELECT m.target,p.source,p.origin,p.version,p.previous_version,pg_temp.cutover_manifest(p.manifest),p.granted,
 p.granted_digest,p.enabled,p.config,p.remote,p.disabled_contributions,p.installed_at,p.updated_at
 FROM public.plugins p JOIN cutover_ids m ON p.id=m.id;
UPDATE public.plugin_secrets s SET plugin=m.target FROM cutover_ids m WHERE s.plugin=m.id;
UPDATE public.plugin_audit SET plugin=pg_temp.cutover_extension_id(plugin)
 WHERE plugin<>pg_temp.cutover_extension_id(plugin);
DELETE FROM public.plugins p USING cutover_ids m WHERE p.id=m.id;
UPDATE public.plugins SET manifest=pg_temp.cutover_manifest(manifest)
 WHERE manifest IS DISTINCT FROM pg_temp.cutover_manifest(manifest);
DO $check$
BEGIN
 IF EXISTS (SELECT 1 FROM public.plugins WHERE id<>pg_temp.cutover_extension_id(id)
   OR manifest IS DISTINCT FROM pg_temp.cutover_manifest(manifest))
   OR EXISTS (SELECT 1 FROM public.plugin_secrets WHERE plugin<>pg_temp.cutover_extension_id(plugin))
   OR EXISTS (SELECT 1 FROM public.plugin_audit WHERE plugin<>pg_temp.cutover_extension_id(plugin)) THEN
   RAISE EXCEPTION 'Old extension or manifest IDs remain';
 END IF;
 IF EXISTS ((SELECT * FROM cutover_secrets EXCEPT SELECT plugin,name,sealed FROM public.plugin_secrets)
   UNION ALL (SELECT plugin,name,sealed FROM public.plugin_secrets EXCEPT SELECT * FROM cutover_secrets)) THEN
   RAISE EXCEPTION 'Sealed secrets changed';
 END IF;
END $check$;
SELECT 'extension IDs and sealed secrets verified' AS verification, count(*) AS renamed FROM cutover_ids;
\endif
""" + "\n".join(_settings_sql(table) for table in ("settings", "hub_settings")) + r"""
\if :cutover_devices
LOCK TABLE public.devices IN ACCESS EXCLUSIVE MODE;
UPDATE public.devices SET prefs=jsonb_set(prefs,'{look,package}',
 to_jsonb(pg_temp.cutover_look_id(prefs#>>'{look,package}')),false)
 WHERE prefs#>>'{look,package}' IS DISTINCT FROM pg_temp.cutover_look_id(prefs#>>'{look,package}');
DO $check$
BEGIN
 IF EXISTS (SELECT 1 FROM public.devices WHERE prefs#>>'{look,package}' IS DISTINCT FROM
   pg_temp.cutover_look_id(prefs#>>'{look,package}')) THEN RAISE EXCEPTION 'Old device look IDs remain'; END IF;
END $check$;
SELECT 'devices.prefs.look.package old IDs' AS verification, count(*) AS remaining FROM public.devices
 WHERE prefs#>>'{look,package}' IS DISTINCT FROM pg_temp.cutover_look_id(prefs#>>'{look,package}');
\endif
\if :cutover_node_sessions
LOCK TABLE public.node_sessions IN ACCESS EXCLUSIVE MODE;
UPDATE public.node_sessions s SET cwd=r.current || substr(s.cwd,length(r.previous)+1)
 FROM cutover_roots r WHERE s.cwd=r.previous OR left(s.cwd,length(r.previous)+1)=r.previous || '/';
DO $check$
BEGIN
 IF EXISTS (SELECT 1 FROM public.node_sessions s JOIN cutover_roots r
   ON s.cwd=r.previous OR left(s.cwd,length(r.previous)+1)=r.previous || '/') THEN
   RAISE EXCEPTION 'Stored session folders remain under a previous root';
 END IF;
END $check$;
SELECT 'node_sessions.cwd under a previous root' AS verification, count(*) AS remaining FROM public.node_sessions s
 JOIN cutover_roots r ON s.cwd=r.previous OR left(s.cwd,length(r.previous)+1)=r.previous || '/';
\endif
COMMIT;
\echo database-cutover: committed; supported ID fields and stored session folders verified; services must remain stopped
""").replace("__SESSION_ROOTS__", roots)


def _settings_sql(table):
    # Table names come from this fixed tuple, never from command-line input.
    return r"""
\if :cutover___TABLE_NAME__
LOCK TABLE public.__TABLE_NAME__ IN ACCESS EXCLUSIVE MODE;
UPDATE public.__TABLE_NAME__ SET value=jsonb_set(value,'{id}',to_jsonb(pg_temp.cutover_look_id(value->>'id')),false)
 WHERE key='look' AND value->>'id' IS DISTINCT FROM pg_temp.cutover_look_id(value->>'id');
DELETE FROM public.__TABLE_NAME__ WHERE key='hub.extensions_report'; -- Regenerated, never served with stale IDs.
DO $check$
BEGIN
 IF EXISTS (SELECT 1 FROM public.__TABLE_NAME__ WHERE key='look' AND value->>'id' IS DISTINCT FROM
   pg_temp.cutover_look_id(value->>'id')) THEN RAISE EXCEPTION 'Old __TABLE_NAME__ look IDs remain'; END IF;
END $check$;
SELECT '__TABLE_NAME__.look old IDs' AS verification,count(*) AS remaining FROM public.__TABLE_NAME__ WHERE key='look'
 AND value->>'id' IS DISTINCT FROM pg_temp.cutover_look_id(value->>'id');
\endif
""".replace("__TABLE_NAME__", table)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-name", required=True, help="previous product's lowercase name; no built-in alias")
    parser.add_argument("--home", required=True, type=Path, help="explicit home directory to inspect, never inferred")
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--apply", action="store_true", help="apply the offline filesystem plan")
    action.add_argument("--rollback", action="store_true", help="reverse this journal, not external database or service edits")
    action.add_argument("--print-database-sql", action="store_true", help="print a transaction for manual reviewed psql execution; never connect")
    parser.add_argument("--records", type=Path, help="optional .persona-cutover-records.json offline database rehearsal")
    parser.add_argument("--managed-harness-json", type=Path, action="append", default=[],
                        help="explicit approved harness JSON below --home; typed MCP/env/permission namespaces only; repeatable")
    parser.add_argument("--claude-config-dir", type=Path, action="append", default=[],
                        help="explicit Claude Code config directory below --home; its conversation directories for folders "
                             "under the previous root move to their new keys; repeatable")
    parser.add_argument("--session-home", action="append", default=[],
                        help="with --print-database-sql: home of another machine whose stored sessions share this database; "
                             "--home is always included; repeatable")
    parser.add_argument("--journal", type=Path, help="new .persona-cutover-<id> private directory directly below --home (required for mutation)")
    parser.add_argument("--approve", action="store_true", help="explicit operator approval; not production authorization")
    parser.add_argument("--services-stopped", action="store_true", help="operator confirms every writer and auto-start path is stopped")
    parser.add_argument("--backup-verified", action="store_true", help="operator confirms independent file/database backups were verified")
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,31}", args.source_name) or args.source_name == "persona":
        parser.error("--source-name must be a distinct lowercase product name")
    if not args.home.is_absolute() or not args.home.is_dir():
        parser.error("--home must be an existing absolute directory")
    home = args.home.resolve(strict=True)
    if (args.apply or args.rollback) and not (args.approve and args.services_stopped and args.journal):
        parser.error("mutation requires --approve, --services-stopped and --journal")
    if args.apply and not args.backup_verified:
        parser.error("apply requires --backup-verified")
    if args.journal and (not args.journal.is_absolute() or ".." in args.journal.parts):
        parser.error("--journal must be an absolute path without traversal")
    journal = home / args.journal.name if args.journal and args.journal.parent.resolve() == home else args.journal
    records = args.records
    if records and (not records.is_absolute() or ".." in records.parts):
        parser.error("--records must be an absolute path without traversal")
    if records and records.parent.resolve() == home:
        records = home / records.name
    harnesses = []
    for path in args.managed_harness_json:
        if not path.is_absolute() or ".." in path.parts:
            parser.error("--managed-harness-json requires an absolute path without traversal")
        # Canonicalize HOME spelling only; never resolve an untrusted child symlink.
        if path.is_relative_to(args.home):
            path = home / path.relative_to(args.home)
        if path in harnesses:
            parser.error("duplicate managed harness path")
        harnesses.append(path)
    claude_dirs = []
    for path in args.claude_config_dir:
        if not path.is_absolute() or ".." in path.parts:
            parser.error("--claude-config-dir requires an absolute path without traversal")
        if path.is_relative_to(args.home):
            path = home / path.relative_to(args.home)
        if path in claude_dirs:
            parser.error("duplicate Claude Code config directory")
        claude_dirs.append(path)
    if args.session_home and not args.print_database_sql:
        parser.error("--session-home only applies to --print-database-sql")
    try:
        if args.print_database_sql:
            # Stored sessions record the home as the node spelled it, so keep --home as given. Windows folders are
            # not rewritten (see the runbook).
            homes = ([] if os.name == "nt" else [os.path.normpath(args.home)]) + args.session_home
            print(database_sql(args.source_name, homes), end="")
        elif args.apply:
            with transaction_lock(home):
                count = apply(home, args.source_name, journal, records, harnesses, claude_dirs)
            print("apply: " + str(count) + " operations; services remain stopped; manual cutover still required")
        elif args.rollback:
            with transaction_lock(home):
                count = rollback(home, args.source_name, journal)
            print("rollback: " + str(count) + " operations reversed; services remain stopped")
        else:
            operations = plan(home, args.source_name, records, harnesses, claude_dirs)
            visible = [{key: value for key, value in op.items() if key in ("action", "path", "to", "mode")}
                       for op in operations]
            manual = ["services", "binary installation", "environment and MCP", "database", "clients"]
            if not claude_dirs:
                manual.append("agent conversation directories (--claude-config-dir)")
            print(json.dumps({"verdict": "plan", "operations": visible, "manual": manual}, indent=2))
        return 0
    except (Refusal, OSError, ValueError, RuntimeError, KeyError, TypeError) as exc:
        # Do not print file contents, credentials, environment or OS exception filenames.
        message = str(exc) if isinstance(exc, Refusal) else type(exc).__name__
        print("refused: " + message + "; no service was started; inspect the private journal if apply began", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
