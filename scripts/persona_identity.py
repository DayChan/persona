#!/usr/bin/env python3
"""Resolve runtime state roots without reading build configuration or credentials."""
import argparse
import base64
import glob
import os
import pathlib
import plistlib
import re
import shlex


# Compatibility spellings live here rather than in deployment callers.
LEGACY_PREFIX = "JARVIS_"
HOME_KEYS = ("PERSONA_HOME", LEGACY_PREFIX + "HOME")
SERVICE_NAMES = {
    "hub": ("personad", "jarvisd"),
    "node": ("personad-node", "jarvisd-node"),
    "link": ("persona-link", "jarvis-link"),
}
LAUNCH_LABELS = {
    "hub": ("com.persona.hub", "com.jarvis.hub"),
    "node": ("com.persona.node", "com.jarvis.node"),
    "link": ("com.persona.link", "com.jarvis.link"),
}
BINARY_NAMES = {"hub": ("personad", "jarvisd"), "node": ("personad", "jarvisd"),
                "link": ("persona-link", "jarvis-link")}
DATABASE_NAMES = ("persona-postgres", "jarvis-postgres")
DATABASE_VOLUMES = ("persona-pgdata", "jarvis-pgdata")


def env_value(name, environment=None):
    environment = os.environ if environment is None else environment
    suffix = name.split("_", 1)[1] if name.startswith(("PERSONA_", LEGACY_PREFIX)) else name
    return environment.get("PERSONA_" + suffix, environment.get(LEGACY_PREFIX + suffix, ""))


def environment_aliases(exclude=(), environment=None):
    """Emit references, never secret values; callers evaluate these in their own shell."""
    environment = os.environ if environment is None else environment
    suffixes = {key.split("_", 1)[1] for key in environment if key.startswith(("PERSONA_", LEGACY_PREFIX))
                and key.isascii() and key.replace("_", "").isalnum()}
    lines = []
    for suffix in sorted(suffixes):
        current, legacy = "PERSONA_" + suffix, LEGACY_PREFIX + suffix
        if current in exclude or legacy in exclude:
            continue
        source = current if current in environment else legacy
        target = legacy if source == current else current
        lines.append('export ' + target + '="${' + source + '}"')
    return "\n".join(lines)


def config_path(environment=None):
    environment = os.environ if environment is None else environment
    if "PERSONA_CONFIG" in environment or LEGACY_PREFIX + "CONFIG" in environment:
        return os.path.expanduser(env_value("CONFIG", environment))
    base = pathlib.Path(environment.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config"))
    current, legacy = base / "persona/config.json", base / "jarvis/config.json"
    return os.fspath(current if os.path.lexists(current) or not os.path.lexists(legacy) else legacy)



def data_dir(home=None, environment=None):
    home = os.path.expanduser("~") if home is None else os.fspath(home)
    root = env_value("PERSONA_HOME", environment)
    if not root:
        current, legacy = os.path.join(home, ".persona"), os.path.join(home, ".jarvis")
        if os.path.lexists(current) and os.path.lexists(legacy):
            try:
                if os.path.samefile(current, legacy):
                    return current
            except OSError:
                pass
            raise OSError("independent state directories exist; set PERSONA_HOME explicitly")
        return legacy if os.path.lexists(legacy) else current
    if root == "~": root = home
    elif root.startswith("~/"): root = os.path.join(home, root[2:])
    elif not os.path.isabs(root): root = os.path.join(home, root)
    return os.path.normpath(root)


def prepare_dir(home=None, environment=None):
    root = data_dir(home, environment)
    # Follow an existing valid custom link, but never create an alias or repair a dangling link.
    if os.path.islink(root) and not os.path.isdir(root):
        raise OSError("state symlink target is unavailable; restore it before upgrading")
    os.makedirs(root, mode=0o700, exist_ok=True)
    home = os.path.expanduser("~") if home is None else os.fspath(home)
    # An installation still kept under the former name gets the current name as an alias; one under the current name
    # gets no alias under the former name (#1445). An alias already there is left as it is.
    current, former = os.path.join(home, ".persona"), os.path.join(home, ".jarvis")
    if root == former:
        alias = current
        if not os.path.lexists(alias):
            try:
                os.symlink(os.path.basename(root), alias)
            except FileExistsError:
                if not os.path.samefile(root, alias):
                    raise OSError("state directories changed during initialization")
    return data_dir(home, environment)


def _file_environment(path, keys=HOME_KEYS):
    """Consume complete systemd-style assignments while retaining only HOME values."""
    values, key, value = {}, [], []
    state, wanted, trailing = "before_key", False, None
    whitespace, newline = " \t\n\r", "\n\r"

    def append(character):
        if wanted:
            value.append(character)

    def emit(trim=False):
        if wanted:
            result = value[:trailing] if trim and trailing is not None else value
            values["".join(key).rstrip(whitespace)] = "".join(result)

    with path.open(newline="") as stream:
        ended = False
        for line in stream:
            for character in line:
                if character == "\0":
                    ended = True
                    break
                if state == "before_key":
                    if character in "#;":
                        state = "comment"
                    elif character not in whitespace:
                        key, state = [character], "key"
                elif state == "key":
                    if character in newline:
                        key, state = [], "before_key"
                    elif character == "=":
                        wanted = "".join(key).rstrip(whitespace) in keys
                        value, trailing, state = [], None, "before_value"
                    else:
                        key.append(character)
                elif state in ("before_value", "plain") and character in newline:
                    emit(trim=state == "plain")
                    key, value, wanted, trailing, state = [], [], False, None, "before_key"
                elif state == "before_value":
                    if character == "'":
                        state = "single"
                    elif character == '"':
                        state = "double"
                    elif character == "\\":
                        state = "plain_escape"
                    elif character not in whitespace:
                        append(character)
                        state = "plain"
                elif state == "plain":
                    if character == "\\":
                        trailing, state = None, "plain_escape"
                    else:
                        if character not in whitespace:
                            trailing = None
                        elif trailing is None:
                            trailing = len(value)
                        append(character)
                elif state == "plain_escape":
                    state = "plain"
                    if character not in newline:
                        append(character)
                elif state == "single":
                    if character == "'":
                        state = "before_value"
                    else:
                        append(character)
                elif state == "double":
                    if character == '"':
                        state = "before_value"
                    elif character == "\\":
                        state = "double_escape"
                    else:
                        append(character)
                elif state == "double_escape":
                    state = "double"
                    if character in ('"', "\\", "`", "$"):
                        append(character)
                    elif character != "\n":
                        append("\\")
                        append(character)
                elif state == "comment":
                    if character == "\\":
                        state = "comment_escape"
                    elif character in newline:
                        state = "before_key"
                elif state == "comment_escape":
                    state = "before_key" if character in newline else "comment"
            if ended:
                break
    if state in ("before_value", "plain", "plain_escape", "single", "double", "double_escape"):
        emit(trim=state == "plain")
    return values


def _unit_words(text):
    """Unquote unit words without turning C-escaped whitespace into word boundaries."""
    decoded, ordinal = {}, 0xE000
    simple = {"a": "\a", "b": "\b", "f": "\f", "n": "\n", "r": "\r", "t": "\t", "v": "\v",
              "s": " ", "e": "\x1b", "\\": "\\", '"': '"', "'": "'"}

    def protect(match):
        nonlocal ordinal
        escape = match.group(0)[1:]
        if escape in simple:
            value = simple[escape].encode("utf-8")
        elif escape[0] in "uU":
            value = chr(int(escape[1:], 16)).encode("utf-8")
        elif escape[0] == "x":
            value = bytes([int(escape[1:], 16)])
        else:
            value = bytes([int(escape, 8)])
        if b"\0" in value:
            raise ValueError("NUL is not valid in a unit environment")
        while chr(ordinal) in text:
            ordinal += 1
        marker = chr(ordinal)
        ordinal += 1
        decoded[marker] = value
        return marker

    encoded = re.sub(r"\\(?:x[0-9a-fA-F]{2}|u[0-9a-fA-F]{4}|U[0-9a-fA-F]{8}|[0-7]{3}|[abfnrtvse\\\"'])", protect, text)
    return [b"".join(decoded.get(character, character.encode("utf-8")) for character in word).decode("utf-8")
            for word in shlex.split(encoded)]


def _unit_environment(unit, home, keys=HOME_KEYS):
    values, environment_files, unset = {}, [], []
    files = [unit, *sorted(unit.with_name(unit.name + ".d").glob("*.conf"))]
    for file in files:
        try:
            lines = file.read_text().splitlines()
        except FileNotFoundError:
            continue
        section, continued = "", ""
        for line in lines:
            line = line.strip()
            if not line or line.startswith(("#", ";")):
                continue
            line = continued + line
            if line.endswith("\\"):
                continued = line[:-1] + " "
                continue
            continued = ""
            if line.startswith("["):
                section = line
                continue
            if section != "[Service]":
                continue
            setting, sep, text = line.partition("=")
            setting = setting.strip()
            if not sep:
                continue
            if setting not in ("Environment", "EnvironmentFile", "UnsetEnvironment"):
                continue
            if setting == "EnvironmentFile":
                if not text.strip():
                    environment_files.clear()
                else:
                    environment_files.append(text.strip())
                continue
            try:
                entries = _unit_words(text)
            except ValueError:
                raise OSError("service environment could not be parsed; set PERSONA_HOME explicitly") from None
            if setting == "UnsetEnvironment":
                if not entries:
                    unset.clear()
                unset.extend(entries)
                continue
            if not entries:
                values.clear()
            for entry in entries:
                key, sep, value = entry.partition("=")
                if sep and key in keys:
                    values[key] = value.replace("%%", "\0").replace("%h", home).replace("\0", "%")
    for entry in environment_files:
        optional = entry.startswith("-")
        pattern = entry[1:] if optional else entry
        pattern = pattern.replace("%%", "\0").replace("%h", home).replace("\0", "%")
        if not os.path.isabs(pattern):
            raise OSError("EnvironmentFile must name an absolute path")
        matches = sorted(glob.glob(pattern))
        if not matches and not optional:
            raise FileNotFoundError("required service environment file is unavailable")
        for path in matches:
            try:
                values.update(_file_environment(pathlib.Path(path), keys))
            except (OSError, UnicodeError):
                if not optional:
                    raise
    for entry in unset:
        entry = entry.replace("%%", "\0").replace("%h", home).replace("\0", "%")
        key, sep, value = entry.partition("=")
        if key in values and (not sep or values[key] == value):
            del values[key]
    return values


def service_environment(home=None, environment=None, role="auto"):
    """Recover only the requested service's root; never borrow a different service's profile."""
    environment = dict(os.environ if environment is None else environment)
    if any(key in environment for key in HOME_KEYS):
        return environment
    home = os.path.expanduser("~") if home is None else os.fspath(home)
    config = pathlib.Path(environment.get("XDG_CONFIG_HOME") or os.path.join(home, ".config"))
    roles = {"hub": ("hub",), "node": ("node", "link"), "link": ("link", "node"),
             "auto": ("hub", "node", "link")}[role]
    units = tuple(name + ".service" for item in roles for name in SERVICE_NAMES[item])
    found = []
    for name in units:
        unit = config / "systemd/user" / name
        installed = unit.exists() or unit.with_name(unit.name + ".d").is_dir()
        values = _unit_environment(unit, home)
        if installed:
            if role != "auto":
                return dict(environment, **values)
            found.append(values)
    if role != "hub":
        names = tuple(name + ".plist" for item in roles for name in LAUNCH_LABELS[item])
        for name in names:
            try:
                stored = plistlib.loads(pathlib.Path(home, "Library/LaunchAgents", name).read_bytes())
            except FileNotFoundError:
                continue
            values = stored.get("EnvironmentVariables", {})
            values = {key: values[key] for key in HOME_KEYS if isinstance(values.get(key), str)}
            if role != "auto":
                return dict(environment, **values)
            found.append(values)
    if found:
        roots = [data_dir(home, values) for values in found]
        for root in roots[1:]:
            try:
                same = root == roots[0] or os.path.samefile(root, roots[0])
            except OSError:
                same = False
            if not same:
                raise OSError("installed services use different state roots; choose --service hub, node or link")
        environment.update(found[0])
    return environment


def service_name(role, home=None, environment=None, launchd=False):
    """Keep an installed service identity; never start both spellings of one role."""
    environment = os.environ if environment is None else environment
    home = pathlib.Path(os.path.expanduser("~") if home is None else home)
    names = LAUNCH_LABELS[role] if launchd else SERVICE_NAMES[role]
    directory = home / "Library/LaunchAgents" if launchd else pathlib.Path(environment.get("XDG_CONFIG_HOME") or home / ".config") / "systemd/user"
    suffix = ".plist" if launchd else ".service"
    installed = [name for name in names if os.path.lexists(directory / (name + suffix)) or
                 (not launchd and (directory / (name + suffix + ".d")).is_dir())]
    if len(installed) > 1:
        raise OSError("both service identities are installed; remove the inactive duplicate explicitly")
    return installed[0] if installed else names[0]


def binary_path(root, role):
    for name in BINARY_NAMES[role]:
        path = os.path.join(root, "bin", name)
        if os.path.isfile(path) and os.access(path, os.X_OK):
            return path
    return os.path.join(root, "bin", BINARY_NAMES[role][0])


def shell_environment(role="auto", prepare=False):
    environment = service_environment(role=role)
    root = prepare_dir(environment=environment) if prepare else data_dir(environment=environment)
    values = {"_PERSONA_STATE_DIR": root, **{key: root for key in HOME_KEYS},
              "_PERSONA_LEGACY_HOME_KEY": HOME_KEYS[1], "_PERSONA_LEGACY_EDGE_KEY": LEGACY_PREFIX + "HUB_EDGE",
              "PERSONA_DB_CONTAINER": DATABASE_NAMES[0], "PERSONA_DB_LEGACY_CONTAINER": DATABASE_NAMES[1],
              "PERSONA_DB_VOLUME": DATABASE_VOLUMES[0], "PERSONA_DB_LEGACY_VOLUME": DATABASE_VOLUMES[1]}
    for item in ("hub", "node", "link") if role == "auto" else (role,):
        values["_PERSONA_" + item.upper() + "_UNIT"] = service_name(item)
        values["_PERSONA_" + item.upper() + "_LABEL"] = service_name(item, launchd=True)
        values["_PERSONA_" + item.upper() + "_BIN"] = binary_path(root, item)
    if role == "hub" and "PERSONA_HUB_EDGE" not in environment and LEGACY_PREFIX + "HUB_EDGE" not in environment:
        unit_dir = pathlib.Path(environment.get("XDG_CONFIG_HOME") or pathlib.Path.home() / ".config") / "systemd/user"
        edge_keys = ("PERSONA_HUB_EDGE", LEGACY_PREFIX + "HUB_EDGE")
        stored = _unit_environment(unit_dir / (values['_PERSONA_HUB_UNIT'] + '.service'), str(pathlib.Path.home()), edge_keys)
        if any(key in stored for key in edge_keys):
            values['PERSONA_HUB_EDGE'] = env_value('HUB_EDGE', stored)
    for suffix in ("HUB_SOCKET", "HUB_EDGE", "SKIP_HERDR", "HERDR_INSTALL"):
        if "PERSONA_" + suffix in environment or LEGACY_PREFIX + suffix in environment:
            values["PERSONA_" + suffix] = env_value(suffix, environment)
    return "\n".join("export " + key + "=" + shlex.quote(value) for key, value in values.items())


def command_aliases(directory):
    directory = pathlib.Path(directory)
    pairs = [("personad", "jarvisd"), ("persona-link", "jarvis-link")]
    pairs += [(path.name, "jarvisd-" + path.name[len("personad-"):])
              for path in directory.glob("personad-*") if not path.name.endswith((".gz", ".prev", ".new"))]
    for current, legacy in pairs:
        current_path = directory / current
        if current_path.is_symlink() or not current_path.is_file():
            continue
        alias = directory / legacy
        if alias.is_symlink() and os.readlink(alias) == current:
            continue
        if alias.is_dir():
            raise OSError("compatibility command is a directory: " + str(alias))
        temporary = directory / (legacy + ".compat-new")
        if os.path.lexists(temporary):
            raise OSError("unfinished compatibility alias exists: " + str(temporary))
        os.symlink(current, temporary)
        os.replace(temporary, alias)


def artifact_aliases(directory):
    import shutil
    directory = pathlib.Path(directory)
    for current, legacy in (("personad-", "jarvisd-"), ("persona-link-", "jarvis-link-")):
        for path in directory.glob(current + "*.gz"):
            target = directory / (legacy + path.name[len(current):])
            shutil.copyfile(path, target)


def render_installer(source, target, url):
    code = base64.b64encode(pathlib.Path(__file__).read_bytes()).decode()
    text = pathlib.Path(source).read_text().replace("@PERSONA_DL@", url).replace("@PERSONA_IDENTITY@", code)
    pathlib.Path(target).write_text(text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--service", nargs="?", const="auto", choices=("auto", "hub", "node", "link"))
    parser.add_argument("--prefix", metavar="SCRIPT")
    parser.add_argument("--shell", action="store_true")
    parser.add_argument("--env", action="store_true")
    parser.add_argument("--alias-binaries", metavar="DIRECTORY")
    parser.add_argument("--artifact-aliases", metavar="DIRECTORY")
    parser.add_argument("--render-installer", nargs=3, metavar=("SOURCE", "TARGET", "URL"))
    args = parser.parse_args()
    if args.alias_binaries:
        command_aliases(args.alias_binaries)
        return
    if args.artifact_aliases:
        artifact_aliases(args.artifact_aliases)
        return
    if args.render_installer:
        render_installer(*args.render_installer)
        return
    if args.prefix:
        # The resolver travels base64-encoded on the python3 -c line: a heredoc inside $(...) breaks macOS's bash 3.2,
        # which mis-parses the backticks and quotes in the code ("unexpected EOF while looking for matching `").
        code = base64.b64encode(pathlib.Path(__file__).read_bytes()).decode()
        print("_PERSONA_ENV=$(python3 -c \"import base64; exec(base64.b64decode('" + code + "'))\" --shell --service "
              + (args.service or "auto") + (" --prepare" if args.prepare else "") + ") || exit $?")
        print('eval "$_PERSONA_ENV"; unset _PERSONA_ENV')
        print("persona_alias_binaries() { python3 -c \"import base64; exec(base64.b64decode('" + code + "'))\" --alias-binaries \"$1\"; }")
        if args.prefix != "-": print(pathlib.Path(args.prefix).read_text(), end="")
        return
    if args.env:
        print(environment_aliases())
        return
    if args.shell:
        print(shell_environment(args.service or "auto", args.prepare))
        return
    environment = service_environment(role=args.service) if args.service else os.environ
    print(prepare_dir(environment=environment) if args.prepare else data_dir(environment=environment))


if __name__ == "__main__":
    main()
