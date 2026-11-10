import importlib.util
from pathlib import Path
import re
import unittest

SPEC = importlib.util.spec_from_file_location("check_android_shrink", Path(__file__).with_name("check_android_shrink.py"))
shrink = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(shrink)
DexClass = shrink.DexClass

# The shape of `aapt2 dump xmltree --file AndroidManifest.xml` for a merged manifest.
MANIFEST = '''N: android=http://schemas.android.com/apk/res/android (line=2)
  E: manifest (line=2)
    A: package="com.example.persona" (Raw: "com.example.persona")
    E: application (line=10)
      A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.android.PersonaApp" (Raw: "persona.android.PersonaApp")
      A: http://schemas.android.com/apk/res/android:appComponentFactory(0x0101057a)="androidx.core.app.CoreComponentFactory" (Raw: "androidx.core.app.CoreComponentFactory")
      E: activity (line=12)
        A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.android.MainActivity" (Raw: "persona.android.MainActivity")
      E: provider (line=20)
        A: http://schemas.android.com/apk/res/android:name(0x01010003)="androidx.startup.InitializationProvider" (Raw: "androidx.startup.InitializationProvider")
        E: meta-data (line=21)
          A: http://schemas.android.com/apk/res/android:name(0x01010003)="androidx.work.WorkManagerInitializer" (Raw: "androidx.work.WorkManagerInitializer")
          A: http://schemas.android.com/apk/res/android:value(0x01010024)="androidx.startup" (Raw: "androidx.startup")
        E: meta-data (line=22)
          A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.example.flag" (Raw: "persona.example.flag")
          A: http://schemas.android.com/apk/res/android:value(0x01010024)="on" (Raw: "on")
'''


BRIDGE = "persona.core.designsystem.MarkdownRenderer$Bridge"
SSH_ALGORITHMS = ("persona.core.hub.tunnel.TinkX25519", "persona.core.hub.tunnel.TinkEd25519", "com.jcraft.jsch.DH25519",
                  "com.jcraft.jsch.UserAuthPublicKey", "com.jcraft.jsch.jce.AES128GCM")

# The shapes of R8's seeds.txt (classes, then `Class: member`) and usage.txt (`Class` removed whole, `Class:` then the
# members removed from a kept one).
SEEDS = """persona.core.hub.HubFailure
persona.core.hub.HubClient$Refused
persona.core.contracts.api.ModelPurposeNotFound
coil3.network.HttpException
persona.core.hub.HubFailure: java.lang.String getCode()
androidx.work.ListenableWorker
"""
USAGE = """persona.core.contracts.api.ModelPurposeNotFound
persona.core.hub.HubClient:
    public void unused()
"""


def kept_release():
    init = {shrink.NO_ARGS}
    return {
        "persona.android.PersonaApp": DexClass("android.app.Application", methods=set(init)),
        "androidx.core.app.CoreComponentFactory": DexClass("android.app.AppComponentFactory", methods=set(init)),
        "persona.android.MainActivity": DexClass("android.app.Activity", methods=set(init)),
        "androidx.startup.InitializationProvider": DexClass("android.content.ContentProvider", methods=set(init)),
        "androidx.work.WorkManagerInitializer": DexClass("java.lang.Object", methods=set(init)),
        BRIDGE: DexClass("java.lang.Object", methods=set(shrink.REQUIRED_MEMBERS[BRIDGE])),
        "persona.core.hub.HubFailure": DexClass(
            "java.io.IOException", methods=set(shrink.REQUIRED_MEMBERS["persona.core.hub.HubFailure"])),
        **{name: DexClass("java.lang.Object", methods=set(init)) for name in SSH_ALGORITHMS},
        "androidx.room.RoomDatabase": DexClass("java.lang.Object", access=shrink.ACC_ABSTRACT),
        "persona.core.data.ItemsDatabase": DexClass("androidx.room.RoomDatabase", access=shrink.ACC_ABSTRACT),
        "persona.core.data.ItemsDatabase_Impl": DexClass("persona.core.data.ItemsDatabase", methods=set(init)),
        "androidx.work.ListenableWorker": DexClass("java.lang.Object", access=shrink.ACC_ABSTRACT),
        "androidx.work.CoroutineWorker": DexClass("androidx.work.ListenableWorker", access=shrink.ACC_ABSTRACT),
        "persona.platform.notify.ConnectionWatchdog": DexClass("androidx.work.CoroutineWorker",
                                                              methods={shrink.WORKER_INIT}),
        "persona.core.navigation.SettingsPageRoute$Storage": DexClass(
            "java.lang.Object", (shrink.NAV_KEY,), annotations={shrink.INNER_CLASS}),
        "persona.core.hub.HubClient$Refused": DexClass("java.io.IOException", annotations={shrink.INNER_CLASS}),
        "coil3.network.HttpException": DexClass("java.lang.RuntimeException"),
        # R8's stub of a platform exception newer than minSdk: no keep rule can list it.
        "android.app.ServiceStartNotAllowedException": DexClass("java.lang.IllegalStateException"),
        # Lost its InnerClass annotation, but neither an exception nor a route: its name is never reported.
        "persona.core.designsystem.Tokens$Space": DexClass("java.lang.Object"),
    }


class ShrinkGuardTests(unittest.TestCase):
    def test_a_release_that_keeps_everything_reached_by_name_passes(self):
        manifest = shrink.manifest_classes(MANIFEST)
        self.assertEqual(manifest, [
            ("application", "persona.android.PersonaApp"),
            ("appComponentFactory", "androidx.core.app.CoreComponentFactory"),
            ("activity", "persona.android.MainActivity"),
            ("provider", "androidx.startup.InitializationProvider"),
            ("startup initializer", "androidx.work.WorkManagerInitializer"),
        ])
        self.assertEqual(shrink.problems(kept_release(), manifest), [])
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=SEEDS, usage=USAGE), [])

    def test_each_removed_keep_stops_the_release(self):
        manifest = shrink.manifest_classes(MANIFEST)
        cases = {
            "Bridge.done": (lambda c: c[BRIDGE].methods.discard(("done", "(ILjava/lang/String;)V")),
                            f"{BRIDGE}.done(ILjava/lang/String;)V: removed"),
            "hub failure status": (lambda c: c["persona.core.hub.HubFailure"].methods.discard(
                ("getStatus", "()Ljava/lang/Integer;")), "HubFailure.getStatus()Ljava/lang/Integer;: removed"),
            "ssh algorithm": (lambda c: c.pop("persona.core.hub.tunnel.TinkX25519"),
                              "persona.core.hub.tunnel.TinkX25519.<init>()V: removed"),
            "activity": (lambda c: c.pop("persona.android.MainActivity"), "activity persona.android.MainActivity"),
            "initializer": (lambda c: c["androidx.work.WorkManagerInitializer"].methods.clear(),
                            "startup initializer androidx.work.WorkManagerInitializer: no-argument constructor"),
            "worker": (lambda c: c["persona.platform.notify.ConnectionWatchdog"].methods.clear(),
                       "worker persona.platform.notify.ConnectionWatchdog"),
            "database": (lambda c: c.pop("persona.core.data.ItemsDatabase_Impl"),
                         "database persona.core.data.ItemsDatabase: generated"),
            "route name": (lambda c: c["persona.core.navigation.SettingsPageRoute$Storage"].annotations.clear(),
                           "SettingsPageRoute$Storage: InnerClass annotation removed"),
            "exception name": (lambda c: c["persona.core.hub.HubClient$Refused"].annotations.clear(),
                               "HubClient$Refused: InnerClass annotation removed"),
        }
        for label, (remove, expected) in cases.items():
            with self.subTest(label):
                classes = kept_release()
                remove(classes)
                found = shrink.problems(classes, manifest)
                self.assertEqual(len(found), 1, found)
                self.assertIn(expected, found[0])
        self.assertEqual(shrink.problems(kept_release(), manifest, "-dontwarn a.B\n"),
                         ["R8 reported missing classes (missing_rules.txt is not empty)"])

    def test_a_throwable_merged_into_another_class_stops_the_release(self):
        # A seed neither in the dex nor removed was merged away: its reports would carry another class's name. A library
        # exception counts as much as persona's (a lateinit crash reported as coil3.network.HttpException).
        manifest = shrink.manifest_classes(MANIFEST)
        merged = SEEDS + "persona.core.contracts.api.ChannelRejected\nkotlin.UninitializedPropertyAccessException\n"
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=merged, usage=USAGE), [
            f"{name}: merged into another class, so reports name that class (keep its name in proguard-rules.pro)"
            for name in ("kotlin.UninitializedPropertyAccessException", "persona.core.contracts.api.ChannelRejected")])
        # A rule that lists only persona Throwables as seeds leaves the merged library ones unseen.
        persona_only = SEEDS.replace("coil3.network.HttpException\n", "")
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=persona_only, usage=USAGE), [
            "Throwables in the dex missing from seeds.txt (1: coil3.network.HttpException): proguard-rules.pro lost "
            "the rule that keeps every Throwable's name"])


    def test_release_is_shrunk_without_obfuscation_and_no_module_pulls_the_extended_icons(self):
        # Shrinking and optimizing take the release APK from about 93 MB to 19.5 MB, and check_android_shrink.py passes an
        # unshrunk APK (the rollback), so only this line notices a merge or an edit that turns it off: a rollback changes
        # it too. Obfuscated or merged classes would break in_app and the names in error reports, and the merge check
        # reads the Throwables the keepnames rule lists as seeds; the extended icons were 21.9 MB of dex in debug builds
        # for one unused button.
        android = Path(__file__).resolve().parents[2] / "android"
        release = re.search(r"\brelease\s*\{([^{}]*)\}", (android / "app/build.gradle.kts").read_text())
        self.assertIsNotNone(release, "no buildTypes release block in android/app/build.gradle.kts")
        self.assertEqual(re.findall(r"\bisMinifyEnabled\s*=\s*(\w+)", release[1]), ["true"])
        self.assertIn('"proguard-rules.pro"', release[1])
        rules = [line.strip() for line in (android / "app/proguard-rules.pro").read_text().splitlines()]
        for rule in ("-dontobfuscate", "-keepattributes SourceFile,LineNumberTable",
                     "-keepattributes InnerClasses,EnclosingMethod",
                     "-keepnames class * extends java.lang.Throwable"):
            self.assertIn(rule, rules)
        # Optimizing saves 13.7 MB: with -dontoptimize the APK is 33.2 MB again (#2840).
        self.assertNotIn("-dontoptimize", rules)
        builds = [android / "gradle/libs.versions.toml", *android.glob("*/build.gradle.kts"),
                  *android.glob("*/*/build.gradle.kts")]
        self.assertEqual([p.name for p in builds if "material-icons-extended" in p.read_text()], [])


if __name__ == "__main__":
    unittest.main()
