#!/usr/bin/env python3
"""Map literal Gradle project includes to disjoint, exhaustive unit-test lanes."""
import argparse
from pathlib import Path
import re
import sys

LANES = ("full", "lint-build", "test-core", "test-feature-0", "test-feature-1", "test-platform-app")
TOKEN = re.compile(r'"(?:\\.|[^"\\])*"|[A-Za-z_][A-Za-z_0-9]*|\S')
PROJECT = re.compile(r":[A-Za-z0-9_-]+(?::[A-Za-z0-9_-]+)*\Z")


def tokens(source):
    """Skip Kotlin comments without mistaking comment markers inside strings."""
    index = 0
    while index < len(source):
        if source[index].isspace():
            index += 1
        elif source.startswith("//", index):
            end = source.find("\n", index)
            index = len(source) if end < 0 else end + 1
        elif source.startswith("/*", index):
            depth = 1
            index += 2
            while depth and index < len(source):
                if source.startswith("/*", index):
                    depth += 1
                    index += 2
                elif source.startswith("*/", index):
                    depth -= 1
                    index += 2
                else:
                    index += 1
            if depth:
                raise ValueError("Unterminated Kotlin comment")
        elif source.startswith('"""', index):
            end = source.find('"""', index + 3)
            if end < 0:
                raise ValueError("Unterminated Kotlin raw string")
            yield source[index:end + 3]
            index = end + 3
        else:
            match = TOKEN.match(source, index)
            yield match.group()
            index = match.end()


def modules(source):
    """Accept literal include lists; fail closed on computed project declarations.

    This is not a Kotlin evaluator. Expressions, interpolation and malformed include
    lists must be supported explicitly before sharded CI can claim full coverage.
    includeBuild is intentionally separate, as in the original root `test` selector.
    """
    parts = list(tokens(source))
    projects = []
    for index, token in enumerate(parts):
        if token != "include":
            continue
        cursor = index + 1
        if cursor >= len(parts) or parts[cursor] != "(":
            raise ValueError("Expected a literal include(...) project declaration")
        cursor += 1
        count = 0
        while cursor < len(parts) and parts[cursor] != ")":
            literal = parts[cursor]
            if not (literal.startswith('"') and literal.endswith('"')):
                raise ValueError("Computed include declarations are not supported")
            project = literal[1:-1]
            if not PROJECT.fullmatch(project):
                raise ValueError("Invalid or computed project path in include declaration")
            projects.append(project)
            count += 1
            cursor += 1
            if cursor < len(parts) and parts[cursor] == ",":
                cursor += 1
            elif cursor >= len(parts) or parts[cursor] != ")":
                raise ValueError("Expected comma or closing parenthesis in include declaration")
        if cursor >= len(parts) or not count:
            raise ValueError("Empty or unterminated include declaration")
    if not projects:
        raise ValueError("No included Android projects found")
    return sorted(set(projects))


def test_lane(project):
    if project.startswith(":core:"):
        return "test-core"
    if project.startswith(":feature:"):
        return "test-feature"
    # App, platform and every new top-level module belong to this fallback lane.
    return "test-platform-app"


def tasks(source, lane):
    if lane not in LANES:
        raise ValueError("Unknown Android CI lane: " + lane)
    projects = modules(source)
    if lane == "full":
        return ["lint", "test", ":app:assembleDebug"]
    if lane == "lint-build":
        return ["lint", ":app:assembleDebug"]
    if lane.startswith("test-feature-"):
        # Split the complete sorted feature set, including future modules, once.
        features = [project for project in projects if test_lane(project) == "test-feature"]
        return [project + ":test" for project in features[int(lane[-1])::2]]
    # The per-project test lifecycle task includes all variants (not only Debug),
    # and works for JVM modules too. No test filters or exclusions are introduced.
    return [project + ":test" for project in projects if test_lane(project) == lane]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--settings", type=Path, default=Path(__file__).resolve().parents[2] / "android/settings.gradle.kts")
    parser.add_argument("--lane", choices=LANES, required=True)
    args = parser.parse_args()
    try:
        selected = tasks(args.settings.read_text(), args.lane)
    except (OSError, ValueError) as error:
        parser.exit(1, "Android CI shards: FAILED (" + str(error) + ")\n")
    for task in selected:
        print(task)


if __name__ == "__main__":
    main()
