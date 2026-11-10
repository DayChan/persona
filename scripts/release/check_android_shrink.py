#!/usr/bin/env python3
"""Check a shrunk release APK for code reached by name (docs/android/android.md, "Release shrinking").

    check_android_shrink.py <release apk> <R8 mapping directory> [--build-tools <dir>]

R8 keeps only what code references. Android also reaches classes by name: manifest components, App Startup
initializers, WorkManager workers (rebuilt from their class name with a (Context, WorkerParameters) constructor), Room
databases (whose generated <name>_Impl is loaded by name), JavaScript bridge methods, the getters the tasks store
looks up on a hub failure and the SSH tunnel's algorithms, which JSch makes from class names. Error reports and breadcrumbs print simple class names, which come from InnerClass
annotations, and the class names themselves, which class merging would replace: every class a keep rule listed in
R8's seeds.txt must still be in the dex or be listed as removed in usage.txt, and every Throwable in the dex must be
listed there (proguard-rules.pro keeps all their names). This reads the merged manifest with aapt2 and every
classes*.dex directly, and exits 1 with one line per problem when anything is missing, so a release stops before
signing.
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import struct
import subprocess
import sys
import zipfile

COMPONENTS = {"application", "activity", "service", "receiver", "provider"}
ANDROID_NAME = "http://schemas.android.com/apk/res/android:name(0x01010003)"
ANDROID_VALUE = "http://schemas.android.com/apk/res/android:value(0x01010024)"
ANDROID_FACTORY = "http://schemas.android.com/apk/res/android:appComponentFactory(0x0101057a)"
NO_ARGS = ("<init>", "()V")
WORKER = "androidx.work.ListenableWorker"
WORKER_INIT = ("<init>", "(Landroid/content/Context;Landroidx/work/WorkerParameters;)V")
NAV_KEY = "androidx.navigation3.runtime.NavKey"
INNER_CLASS = "dalvik.annotation.InnerClass"
ROOM_DATABASE = "androidx.room.RoomDatabase"
PLATFORM = ("android.", "java.", "javax.")
REQUIRED_MEMBERS = {
    # Reached only from JavaScript (MarkdownRenderer.page: addJavascriptInterface(Bridge, "PersonaRender")).
    "persona.core.designsystem.MarkdownRenderer$Bridge": {("done", "(ILjava/lang/String;)V"),
                                                          ("fail", "(ILjava/lang/String;)V")},
    # Looked up by name on a failed tasks call (TasksStore.reloadTodos, isLegacySpacesRoute in core/domain).
    "persona.core.hub.HubFailure": {("getStatus", "()Ljava/lang/Integer;"), ("getCode", "()Ljava/lang/String;")},
    # Made from class names in JSch's config (the SSH tunnel, SshAlgorithms): one of each kind proguard-rules.pro keeps.
    "persona.core.hub.tunnel.TinkX25519": {NO_ARGS},
    "persona.core.hub.tunnel.TinkEd25519": {NO_ARGS},
    "com.jcraft.jsch.DH25519": {NO_ARGS},
    "com.jcraft.jsch.UserAuthPublicKey": {NO_ARGS},
    "com.jcraft.jsch.jce.AES128GCM": {NO_ARGS},
}
ACC_INTERFACE, ACC_ABSTRACT = 0x200, 0x400
NO_INDEX = 0xFFFFFFFF


class DexClass:
    """What the checks need of one class: supertypes, access flags, methods as (name, descriptor), annotation types."""

    def __init__(self, superclass=None, interfaces=(), access=0, methods=None, annotations=None):
        self.superclass, self.interfaces, self.access = superclass, tuple(interfaces), access
        self.methods = set(methods or ())
        self.annotations = set(annotations or ())


def uleb(data, offset):
    value = shift = 0
    while True:
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if byte < 0x80:
            return value, offset
        shift += 7


def read_dex(data):
    """Classes of one dex file by binary name (`a.b.Outer$Inner`), with what the checks need."""
    if data[:4] != b"dex\n" or data[4:7] not in (b"035", b"037", b"038", b"039", b"040"):
        raise ValueError(f"unsupported dex header {data[:8]!r}")
    (_, string_off, _, type_off, _, proto_off, _, _, _, method_off, class_count, class_off) = struct.unpack_from(
        "<12I", data, 56)
    strings = {}

    def string(index):
        if index not in strings:
            offset = struct.unpack_from("<I", data, string_off + 4 * index)[0]
            offset = uleb(data, offset)[1]
            strings[index] = data[offset:data.index(b"\0", offset)].decode("utf-8", "replace")
        return strings[index]

    def type_name(index):
        descriptor = string(struct.unpack_from("<I", data, type_off + 4 * index)[0])
        return descriptor[1:-1].replace("/", ".") if descriptor.startswith("L") else descriptor

    def type_list(offset):
        if not offset:
            return ()
        size = struct.unpack_from("<I", data, offset)[0]
        return struct.unpack_from(f"<{size}H", data, offset + 4)

    def descriptor(proto):
        _, returns, parameters = struct.unpack_from("<3I", data, proto_off + 12 * proto)
        def raw(index):
            return string(struct.unpack_from("<I", data, type_off + 4 * index)[0])

        return "(" + "".join(raw(t) for t in type_list(parameters)) + ")" + raw(returns)

    classes = {}
    for number in range(class_count):
        (index, access, superclass, interfaces, _, annotations_off, data_off, _) = struct.unpack_from(
            "<8I", data, class_off + 32 * number)
        item = DexClass(None if superclass == NO_INDEX else type_name(superclass),
                        tuple(type_name(t) for t in type_list(interfaces)), access)
        if annotations_off:
            set_off = struct.unpack_from("<I", data, annotations_off)[0]
            if set_off:
                size = struct.unpack_from("<I", data, set_off)[0]
                for entry in struct.unpack_from(f"<{size}I", data, set_off + 4):
                    item.annotations.add(type_name(uleb(data, entry + 1)[0]))  # skip the visibility byte
        if data_off:
            counts = []
            offset = data_off
            for _ in range(4):
                count, offset = uleb(data, offset)
                counts.append(count)
            for _ in range(counts[0] + counts[1]):  # fields: index diff, access
                offset = uleb(data, uleb(data, offset)[1])[1]
            for count in counts[2:]:  # direct, then virtual methods: index diff, access, code
                method = 0
                for _ in range(count):
                    diff, offset = uleb(data, offset)
                    method += diff
                    offset = uleb(data, uleb(data, offset)[1])[1]
                    _, proto, name = struct.unpack_from("<HHI", data, method_off + 8 * method)
                    item.methods.add((string(name), descriptor(proto)))
        classes[type_name(index)] = item
    return classes


def read_apk(apk):
    classes = {}
    with zipfile.ZipFile(apk) as archive:
        names = [n for n in archive.namelist() if re.fullmatch(r"classes\d*\.dex", n)]
        if not names:
            raise ValueError(f"{apk}: no classes.dex")
        for name in names:
            classes.update(read_dex(archive.read(name)))
    return classes


def manifest_classes(xmltree):
    """(element, class) pairs from `aapt2 dump xmltree --file AndroidManifest.xml`: components, the application's
    component factory and App Startup initializers (meta-data whose value is `androidx.startup`)."""
    found, element, meta = [], None, {}
    attribute = re.compile(r'\s*A: (\S+)="([^"]*)"')
    for line in xmltree.splitlines():
        start = re.match(r"\s*E: (\S+)", line)
        if start:
            element, meta = start[1], {}
            continue
        match = attribute.match(line)
        if not match:
            continue
        name, value = match[1], match[2]
        if name == ANDROID_NAME and element in COMPONENTS:
            found.append((element, value))
        elif name == ANDROID_FACTORY and element == "application":
            found.append(("appComponentFactory", value))
        elif element == "meta-data" and name in (ANDROID_NAME, ANDROID_VALUE):
            meta[name] = value
            if meta.get(ANDROID_VALUE) == "androidx.startup" and ANDROID_NAME in meta:
                found.append(("startup initializer", meta[ANDROID_NAME]))
    return found


def ancestors(classes, name):
    """Superclasses of [name] inside the APK, then the first one outside it (a platform class)."""
    seen = []
    while name in classes and classes[name].superclass and name not in seen:
        seen.append(name)
        name = classes[name].superclass
        yield name


def implements(classes, name, interface, seen=None):
    seen = set() if seen is None else seen
    if name in seen or name not in classes:
        return False
    seen.add(name)
    item = classes[name]
    return interface in item.interfaces or any(
        implements(classes, other, interface, seen) for other in (*item.interfaces, item.superclass) if other)


def is_throwable(classes, name):
    # Platform exception and error classes all end that way; the app's own are inside the APK.
    chain = list(ancestors(classes, name))
    return bool(chain) and (chain[-1] == "java.lang.Throwable" or chain[-1].endswith(("Exception", "Error")))


def member_class(name):
    """A nested class with a name of its own (`Outer$Inner`), not an anonymous or synthetic one."""
    tail = name.rsplit(".", 1)[-1]
    return "$" in tail and re.fullmatch(r"[A-Za-z_][\w]*", tail.rsplit("$", 1)[1]) is not None and "$$" not in tail


def seed_classes(seeds):
    """Classes (not members) R8's seeds.txt lists: those a keep rule matched and the first tree shaking kept."""
    return {line.strip() for line in seeds.splitlines() if line.strip() and ":" not in line}


def removed_classes(usage):
    """Whole classes R8's usage.txt lists as removed (`Name`; `Name:` heads the members removed from a kept class)."""
    return {line.strip() for line in usage.splitlines()
            if line.strip() and not line[0].isspace() and not line.rstrip().endswith(":")}


def merged_seeds(classes, seeds, usage):
    """Problems with the classes a keep rule listed in seeds.txt. One that is neither in the dex nor removed was merged
    into another class, so a report would name that class instead. proguard-rules.pro keeps every Throwable's name, the
    libraries' too, which lists them all as seeds for this check; a Throwable in the dex that is not a seed means that
    rule is gone or narrower, and the merged ones it no longer lists would pass unseen."""
    listed = seed_classes(seeds)
    removed = removed_classes(usage)
    found = [f"{name}: merged into another class, so reports name that class (keep its name in proguard-rules.pro)"
             for name in sorted(listed) if name not in classes and name not in removed]
    # R8 adds stubs of platform exceptions newer than minSdk (android.app.ServiceStartNotAllowedException): no program
    # class, so no keep rule lists them.
    unlisted = sorted(n for n in classes if n not in listed and not n.startswith(PLATFORM)
                      and is_throwable(classes, n))
    if unlisted:
        examples = ", ".join(unlisted[:3]) + (", ..." if len(unlisted) > 3 else "")
        found.append(f"Throwables in the dex missing from seeds.txt ({len(unlisted)}: {examples}): "
                     "proguard-rules.pro lost the rule that keeps every Throwable's name")
    return found


def problems(classes, manifest, missing_rules="", seeds=None, usage=""):
    """Everything reached by name that R8 removed or renamed. seeds and usage are R8's seeds.txt and usage.txt (None:
    no R8 output, an unshrunk APK)."""
    found = []
    if missing_rules.strip():
        found.append("R8 reported missing classes (missing_rules.txt is not empty)")
    if not manifest:
        found.append("the manifest names no components")
    for element, name in manifest:
        if name not in classes:
            found.append(f"{element} {name}: class removed")
        elif NO_ARGS not in classes[name].methods:
            found.append(f"{element} {name}: no-argument constructor removed")
    for name, members in sorted(REQUIRED_MEMBERS.items()):
        for method, descriptor in sorted(members - (classes[name].methods if name in classes else set())):
            found.append(f"{name}.{method}{descriptor}: removed (called by name)")
    workers = [n for n, c in classes.items()
               if not c.access & (ACC_ABSTRACT | ACC_INTERFACE) and WORKER in ancestors(classes, n)]
    if not any(n.startswith("persona.") for n in workers):
        found.append(f"no {WORKER} subclass of the app is left")
    for name in sorted(workers):
        if WORKER_INIT not in classes[name].methods:
            found.append(f"worker {name}: (Context, WorkerParameters) constructor removed")
    for name, item in sorted(classes.items()):
        if name.startswith("persona.") and item.access & ACC_ABSTRACT and ROOM_DATABASE in ancestors(classes, name):
            impl = classes.get(name + "_Impl")
            if impl is None or NO_ARGS not in impl.methods:
                found.append(f"database {name}: generated {name}_Impl or its no-argument constructor removed")
    for name, item in sorted(classes.items()):
        if (name.startswith("persona.") and member_class(name) and INNER_CLASS not in item.annotations
                and (is_throwable(classes, name) or implements(classes, name, NAV_KEY))):
            found.append(f"{name}: InnerClass annotation removed, so its simple name in reports changes")
    if seeds is not None:
        found += merged_seeds(classes, seeds, usage)
    return found


def build_tools(explicit):
    if explicit:
        return Path(explicit)
    sdk = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    local = Path(__file__).resolve().parents[2] / "android/local.properties"
    if not sdk and local.exists():
        sdk = next((line.split("=", 1)[1].strip() for line in local.read_text().splitlines()
                    if line.startswith("sdk.dir=")), None)
    if not sdk:
        raise SystemExit("check_android_shrink: set ANDROID_HOME or pass --build-tools")
    versions = sorted((p for p in (Path(sdk) / "build-tools").glob("*") if (p / "aapt2").exists()),
                      key=lambda p: [int(x) if x.isdigit() else x for x in re.split(r"[.-]", p.name)])
    if not versions:
        raise SystemExit(f"check_android_shrink: no build-tools with aapt2 under {sdk}")
    return versions[-1]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("apk", type=Path)
    parser.add_argument("mapping", type=Path, help="R8 output directory (build/outputs/mapping/release)")
    parser.add_argument("--build-tools", help="Android build-tools directory holding aapt2")
    args = parser.parse_args(argv)
    if not (args.mapping / "usage.txt").exists():
        # isMinifyEnabled = false, a deliberate rollback (test_check_android_shrink.py pins true): nothing was removed,
        # the checks below still run on the APK.
        print(f"check_android_shrink: no R8 output in {args.mapping}, checking the APK only")
    aapt2 = build_tools(args.build_tools) / "aapt2"
    xmltree = subprocess.run([str(aapt2), "dump", "xmltree", "--file", "AndroidManifest.xml", str(args.apk)],
                             check=True, capture_output=True, text=True).stdout
    def output(name, default):
        path = args.mapping / name
        return path.read_text() if path.exists() else default

    shrunk = (args.mapping / "usage.txt").exists()
    found = problems(read_apk(args.apk), manifest_classes(xmltree), output("missing_rules.txt", ""),
                     # R8 always writes seeds.txt next to usage.txt; an empty one fails the Throwable listing check.
                     output("seeds.txt", "") if shrunk else None, output("usage.txt", ""))
    for line in found:
        print("check_android_shrink: " + line, file=sys.stderr)
    print(f"check_android_shrink: {'FAIL' if found else 'PASS'} {args.apk.name}")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
