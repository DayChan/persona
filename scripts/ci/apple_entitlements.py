"""Prepare only snapshot Mac entitlements for profile-free offline test hosts."""

import argparse
from pathlib import Path
import plistlib


PROFILE_ENTITLEMENTS = frozenset({
    "com.apple.developer.aps-environment",
    "com.apple.developer.usernotifications.time-sensitive",
    "keychain-access-groups",
})


def offline_entitlements(entitlements):
    if entitlements.get("com.apple.security.app-sandbox") is not True:
        raise ValueError("offline Mac test hosts must retain the app sandbox")
    return {key: value for key, value in entitlements.items()
            if key not in PROFILE_ENTITLEMENTS}


def prepare(paths):
    # Validate every input before writing; never remove other permissions or
    # modify the checked-in project. The caller passes disposable snapshot files.
    updates = []
    for path in paths:
        original = plistlib.loads(path.read_bytes())
        prepared = offline_entitlements(original)
        updates.append((path, original, prepared))
    for path, original, prepared in updates:
        path.write_bytes(plistlib.dumps(prepared))
        removed = sorted(original.keys() - prepared.keys())
        print(f"apple-ci: offline {path.name}: removed profile-only entitlements {removed}; sandbox retained")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("entitlements", nargs="+", type=Path)
    args = parser.parse_args()
    prepare(args.entitlements)


if __name__ == "__main__":
    main()
