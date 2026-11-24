import unittest

from ci_scope import CHECKS, checks_for_path, checks_for_range


class CIScopeTests(unittest.TestCase):
    def test_component_paths(self):
        cases = {
            "projects_sidebar.go": {"desktop"},
            "locales/en-US.json": {"desktop"},
            "internal/rexapi/testdata/rex-api-help.json": {"go"},
            "web/src/main.ts": {"web"},
            "web/tailnet/main.go": {"web"},
            "ios/GoRex/GoRexApp.swift": {"ios"},
            "ios/GoRexWidgets/GoRexWidgets.swift": {"ios"},
            "ios/GoRexUITests/HostDragTests.swift": {"ios"},
            "ios/THIRD_PARTY_NOTICES.md": {"ios"},
            "ios/WhatToTest.md": {"notes"},
            "ios/scripts/upload": {"workflow"},
            "README.md": set(),
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(checks_for_path(path), expected)

    def test_cross_component_inputs(self):
        cases = {
            "go.mod": {"go", "web", "ios"},
            "ios/GoRex/ghostty-themes.json": {"web", "ios"},
            "assets/icons/folder.svg": {"desktop", "web"},
            "assets/fonts/JetBrainsMono-Regular.ttf": {"desktop", "web", "ios"},
            "tools/mkicon/main.go": {"go", "web"},
            "internal/tailnode/node.go": {"go", "ios"},
            "internal/pairaddr/pairaddr.go": {"go", "ios"},
            "ios/share/share.go": {"go", "ios"},
            "internal/rexproto/protocol.json": {"go", "ios", "web"},
            "web/src/rex-protocol.gen.ts": {"go", "web"},
            "ios/GoRex/Rex/RexProtocol.gen.swift": {"go", "ios"},
            ".github/workflows/ios-test.yml": {"workflow", "ios"},
            ".github/workflows/testflight.yml": {"workflow"},
            ".github/actions/web-build/action.yml": {"workflow", "web"},
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(checks_for_path(path), expected)

    def test_unknown_paths_and_unavailable_baselines_run_all(self):
        self.assertEqual(checks_for_path("future/config.toml"), set(CHECKS))
        self.assertEqual(checks_for_path(".github/workflows/ci.yml"), set(CHECKS))
        self.assertEqual(checks_for_range("")[0], set(CHECKS))
        self.assertEqual(checks_for_range("0000000000000000000000000000000000000000")[0], set(CHECKS))


if __name__ == "__main__":
    unittest.main()
