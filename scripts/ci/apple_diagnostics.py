"""Collect only Apple failure diagnostics, never source/configuration or keys."""

import argparse
from pathlib import Path
import shutil
import subprocess

import apple_launch_diagnostics


LOGS = ("simulator.log", "simulator-boot.log", "simulator-readiness.log")


def collect(output, destination, runner=subprocess.run):
    destination.mkdir(parents=True, exist_ok=True)
    for path in [output / name for name in LOGS] + sorted(output.glob("runtimes-*.json")):
        if path.is_file() and not path.is_symlink():
            shutil.copyfile(path, destination / path.name)
    for bundle in sorted(output.glob("*.xcresult")):
        if bundle.is_dir() and not bundle.is_symlink():
            # Do not follow links out of a generated result bundle.
            shutil.copytree(bundle, destination / bundle.name, dirs_exist_ok=True,
                            ignore=lambda directory, names: [name for name in names
                                if (Path(directory) / name).is_symlink()])
    apple_launch_diagnostics.collect(output, destination)
    commands = [
        ("simctl-list", ["xcrun", "simctl", "list", "--json"], 120, "json"),
        ("disk-space", ["df", "-h", str(output)], 30, "txt"),
    ]
    for name, command, timeout, suffix in commands:
        try:
            result = runner(command, capture_output=True, text=True, timeout=timeout)
            (destination / f"{name}.{suffix}").write_text(result.stdout)
            (destination / f"{name}.stderr.txt").write_text(result.stderr)
            (destination / f"{name}.status.txt").write_text(str(result.returncode) + "\n")
        except (OSError, subprocess.SubprocessError) as error:
            (destination / f"{name}.stderr.txt").write_text(str(error) + "\n")
            (destination / f"{name}.status.txt").write_text("unavailable\n")
    print(f"apple-ci: failure diagnostics: {destination}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    collect(args.output, args.destination)


if __name__ == "__main__":
    main()
