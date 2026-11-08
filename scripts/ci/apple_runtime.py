"""Select an installed stable Apple runtime compatible with Xcode 26.0."""

import argparse
import json
from pathlib import Path
import re
import sys


def select(runtimes, platform="iOS"):
    compatible = []
    for runtime in runtimes:
        version = runtime.get("version", "")
        identifier = runtime.get("identifier", "")
        name = runtime.get("name", "").lower()
        if (runtime.get("isAvailable") is True
                and identifier.startswith("com.apple.CoreSimulator.SimRuntime." + platform + "-")
                and re.fullmatch(r"26\.0(?:\.[0-9]+)*", version)
                and "beta" not in name and "release candidate" not in name):
            compatible.append(runtime)
    return max(compatible, key=lambda runtime: tuple(int(part) for part in runtime["version"].split(".")))["identifier"] if compatible else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("runtimes_json")
    parser.add_argument("--platform", choices=("iOS", "watchOS"), default="iOS")
    args = parser.parse_args()
    selected = select(json.loads(Path(args.runtimes_json).read_text())["runtimes"], args.platform)
    if not selected:
        sys.exit("apple-ci: FAIL no available stable " + args.platform + " 26.0.x runtime; do not substitute a newer platform")
    print(selected)


if __name__ == "__main__":
    main()
