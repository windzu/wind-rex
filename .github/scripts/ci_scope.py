"""Select CI checks for repository paths, including cross-platform inputs."""

import argparse
import os
import subprocess
from pathlib import Path


CHECKS = ("desktop", "go", "web", "ios", "workflow", "notes")
IOS_NOTES = "ios/WhatToTest.md"
WEB_THEMES = "ios/GoRex/ghostty-themes.json"
SWIFT_PROTOCOL = "ios/GoRex/Rex/RexProtocol.gen.swift"
WEB_PROTOCOL = "web/src/rex-protocol.gen.ts"


def checks_for_path(path):
    if path == IOS_NOTES:
        return {"notes"}
    if path == WEB_THEMES:
        return {"ios", "web"}
    if path == SWIFT_PROTOCOL:
        return {"ios", "go"}
    if path == WEB_PROTOCOL:
        return {"web", "go"}
    if path == "assets/fonts/JetBrainsMono-Regular.ttf":
        return {"desktop", "web", "ios"}
    if path in ("go.mod", "go.sum"):
        return {"go", "web", "ios"}
    if path == ".golangci.yml":
        return {"go"}
    if path == ".github/workflows/ios-test.yml":
        return {"workflow", "ios"}
    if path == ".github/workflows/web.yml" or path.startswith(".github/actions/web-build/"):
        return {"workflow", "web"}
    if path == ".github/workflows/ci.yml" or path in (
        ".github/scripts/ci_scope.py",
        ".github/scripts/test_ci_scope.py",
    ):
        return set(CHECKS)
    if path.startswith((".github/workflows/", ".github/scripts/")):
        return {"workflow"}
    if path.startswith("internal/rexproto/"):
        return {"go", "web", "ios"}
    if path.startswith(("internal/tailnode/", "internal/pairaddr/", "ios/share/", "ios/tailnet/")):
        return {"go", "ios"}
    if path.startswith("tools/mkicon/"):
        return {"go", "web"}
    if path.startswith(("internal/", "tools/", "packaging/")):
        return {"go"}
    if path.startswith("assets/"):
        return {"desktop", "web"}
    if "/" not in path and path.endswith(".go"):
        return {"desktop"}
    if path == "mygo.json" or path.startswith(("locales/", "resources/")):
        return {"desktop"}
    if path.startswith("web/"):
        return {"web"}
    if path.startswith("ios/scripts/") and path.rsplit("/", 1)[-1] in (
        "asc-env", "test-notes", "testflight", "upload",
    ):
        return {"workflow", "notes"} if path.endswith(("/test-notes", "/asc-env")) else {"workflow"}
    if path.startswith("ios/"):
        return {"ios"}
    if path.startswith(("docs/", ".agents/", ".claude/")) or path.endswith(".md") or path == ".gitignore":
        return set()
    return set(CHECKS)


def checks_for_range(base):
    if not base:
        return set(CHECKS), "full run"

    resolved = subprocess.run(
        ["git", "rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}"],
        capture_output=True, text=True, check=False,
    )
    if resolved.returncode != 0:
        return set(CHECKS), "base is unavailable"
    commit = resolved.stdout.strip()
    ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", commit, "HEAD"], check=False)
    if ancestor.returncode != 0:
        return set(CHECKS), "base is not an ancestor"

    # Treat renames as delete plus add so a move checks both components.
    diff = subprocess.check_output(["git", "diff", "--no-renames", "--name-only", "-z", commit, "HEAD", "--"])
    paths = [os.fsdecode(path) for path in diff.split(b"\0") if path]
    selected = set().union(*(checks_for_path(path) for path in paths))
    return selected, f"{len(paths)} changed path(s)"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="", help="Ancestor commit to compare to HEAD. Omit for a full run.")
    args = parser.parse_args()
    selected, reason = checks_for_range(args.base)
    outputs = "\n".join(f"{check}={str(check in selected).lower()}" for check in CHECKS) + "\n"
    print(outputs, end="")
    if os.environ.get("GITHUB_OUTPUT"):
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
            output.write(outputs)
    summary = f"CI scope ({reason}): {', '.join(sorted(selected)) or 'no component checks'}"
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as output:
            output.write(f"### {summary}\n")


if __name__ == "__main__":
    main()
