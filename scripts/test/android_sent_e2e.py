#!/usr/bin/env python3
"""Drive the installed Android app against a mock hub and capture the nested ledger.

Use an owned emulator and an isolated application ID. No production pairing or writes.
"""
import argparse
from pathlib import Path
import re
import subprocess
import time
import xml.etree.ElementTree as ET


class Device:
    def __init__(self, args):
        self.args = args
        self.prefix = [args.adb, "-s", args.serial]
        self.paired = False

    def adb(self, *args, binary=False):
        result = subprocess.run(self.prefix + list(args), check=True, capture_output=True, timeout=30)
        return result.stdout if binary else result.stdout.decode().strip()

    def nodes(self):
        self.adb("shell", "uiautomator", "dump", "/data/local/tmp/persona-sent.xml")
        xml = self.adb("exec-out", "cat", "/data/local/tmp/persona-sent.xml")
        return list(ET.fromstring(xml).iter("node"))

    def find(self, label, *, clickable=False, description=False):
        field = "content-desc" if description else "text"
        nodes = self.nodes()
        parents = {child: node for node in nodes for child in node}
        for node in nodes:
            if node.get(field) != label:
                continue
            candidate = node
            while clickable and candidate.get("clickable") != "true" and candidate in parents:
                candidate = parents[candidate]
            if not clickable or candidate.get("clickable") == "true":
                return candidate
        return None

    def wait(self, label, *, clickable=False, description=False):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            node = self.find(label, clickable=clickable, description=description)
            if node is not None:
                return node
            time.sleep(0.3)
        raise AssertionError(f"Timed out waiting for {label}")

    def tap(self, node):
        x1, y1, x2, y2 = map(int, re.findall(r"\d+", node.get("bounds")))
        self.adb("shell", "input", "tap", str((x1 + x2) // 2), str((y1 + y2) // 2))
        time.sleep(0.5)

    def back(self):
        self.tap(self.wait("返回", description=True))

    def launch(self, style, theme, *, page=None, link=None, cold=True):
        if cold:
            self.adb("shell", "am", "force-stop", self.args.package)
        if link:
            # Real external/notification ACTION_VIEW path, with no DEBUG resolver overriding intent.data.
            self.adb("shell", "am", "start", "-W", "-n", f"{self.args.package}/persona.android.MainActivity",
                     "-a", "android.intent.action.VIEW", "-d", f"'{link}'")
            time.sleep(2)
            return
        command = ["shell", "am", "start", "-W", "-n", f"{self.args.package}/persona.android.debug.DebugEntryActivity",
                   "--es", "debug.automation", "true", "--es", "debug.style", style,
                   "--es", "debug.theme", theme]
        if not self.paired:
            command += ["--es", "debug.mockAddress", self.args.mock]
            self.paired = True
        if page:
            command += ["--es", "debug.openPage", page]
        else:
            command += ["--es", "debug.openTab", "Settings"]
        self.adb(*command)
        time.sleep(2)

    def screenshot(self, name):
        data = self.adb("exec-out", "screencap", "-p", binary=True)
        assert data.startswith(b"\x89PNG\r\n\x1a\n"), "Not a PNG"
        target = self.args.out / f"{name}.png"
        target.write_bytes(data)
        print(f"SCREENSHOT {target.name}", flush=True)


def run(args):
    device = Device(args)
    device.adb("shell", "cmd", "locale", "set-app-locales", args.package, "--locales", "zh-CN")
    title = "以你的名义发出的记录"
    ledger_marker = "全部"
    args.out.mkdir(parents=True, exist_ok=True)
    for style in ("glass", "brief", "board"):
        for theme in ("light", "dark"):
            device.launch(style, theme)
            device.wait("设置")
            settings_tab = device.find("设置", clickable=True)
            if settings_tab is not None:
                device.tap(settings_tab)
            # Traverse the real Settings list, rather than directly opening the new page.
            for _ in range(10):
                assert device.find(title) is None, "Ledger still appears on Settings home"
                entry = device.find("连接器", clickable=True)
                if entry is not None:
                    break
                device.adb("shell", "input", "swipe", "540", "1900", "540", "600", "350")
            else:
                raise AssertionError("Missing Connectors category")
            device.screenshot(f"{style}-{theme}-settings")
            device.tap(entry)
            device.wait(title)
            # Scroll to the bottom so the entry is proved to follow all connectors.
            device.adb("shell", "input", "swipe", "540", "1900", "540", "650", "350")
            device.wait(title)
            device.screenshot(f"{style}-{theme}-connectors")
            device.tap(device.wait(title))
            device.wait(ledger_marker)
            device.screenshot(f"{style}-{theme}-ledger")
            device.back()
            device.wait(title)
            device.back()
            assert device.find(title) is None, "Back did not return to Settings home"
            print(f"PASS hierarchy/back {style}/{theme}", flush=True)
            for page in ("sent", "connectors/sent"):
                link = f"persona://open?target=settings&page={page}"
                for cold in (True, False):
                    device.launch(style, theme, link=link, cold=cold)
                    device.wait(ledger_marker)
                    device.back()
                    device.wait(title)
                print(f"PASS legacy/canonical cold/warm {page} {style}/{theme}", flush=True)
    print("ANDROID_SENT_E2E PASS", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--adb", required=True)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--package", default="com.example.persona.shots")
    parser.add_argument("--mock", required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if not args.serial.startswith("emulator-") or not args.package.startswith("com.example.persona"):
        parser.error("Use an owned emulator and isolated example application ID")
    try:
        run(args)
    except (AssertionError, subprocess.SubprocessError, ET.ParseError) as error:
        print(f"ANDROID_SENT_E2E FAIL: {error}", flush=True)
        raise SystemExit(1) from error


if __name__ == "__main__":
    main()
