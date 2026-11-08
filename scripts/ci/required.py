#!/usr/bin/env python3
import json
import os
import sys


def failures(needs):
    bad = []
    if needs.get("changes", {}).get("result") != "success":
        bad.append("repository guards/path selection did not succeed")
    selected = needs.get("changes", {}).get("outputs", {})
    # apple is the required iOS/Watch/HTML lane. By maintainer decision the
    # independently reported apple_macos lane temporarily does not block merges.
    for platform in ("server", "web", "android", "apple", "runtimes", "scripts"):
        expected = selected.get(platform)
        result = needs.get(platform, {}).get("result")
        if expected == "true" and result != "success":
            bad.append(f"{platform}: selected but {result}")
        elif expected == "false" and result != "skipped":
            bad.append(f"{platform}: not selected but {result}")
        elif expected not in {"true", "false"}:
            bad.append(f"{platform}: missing path selection")
    return bad


if __name__ == "__main__":
    needs = json.loads(os.environ["CI_NEEDS"])
    print(json.dumps(needs, indent=2, sort_keys=True))
    bad = failures(needs)
    for problem in bad:
        print(f"FAIL {problem}")
    print("FAIL CI required" if bad else "PASS CI required")
    sys.exit(bool(bad))
