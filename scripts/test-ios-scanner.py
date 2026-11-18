#!/usr/bin/env python3
"""Run the real scanner-sheet cancellation check in an isolated iOS test host.

Usage: DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
         python3 scripts/test-ios-scanner.py <simulator-UDID>
"""
import json
import pathlib
import subprocess
import sys
import tempfile

repo = pathlib.Path(__file__).resolve().parent.parent
if len(sys.argv) != 2:
    raise SystemExit("Provide an available iPhone simulator UDID from xcrun simctl list devices available")
with tempfile.TemporaryDirectory(prefix="blooper-scanner-tests-") as temporary:
    folder = pathlib.Path(temporary)
    # JSON is also valid YAML; absolute paths keep the generated project isolated.
    spec = {
        "name": "ScannerHarness",
        "options": {"deploymentTarget": {"iOS": "17.0"}},
        "targets": {
            "ScannerHarness": {
                "type": "application", "platform": "iOS",
                "sources": [str(repo / "ios/BlooperRemote/Sources/Platform.swift"), str(repo / "ios/Tests/ScannerHarness.swift")],
                "settings": {"base": {"PRODUCT_BUNDLE_IDENTIFIER": "com.blooper.scanner-harness", "SWIFT_VERSION": "5.9"}},
                "info": {"path": "Info.plist", "properties": {"NSCameraUsageDescription": "Tests the scanner screen.", "UILaunchScreen": {}}},
            },
            "ScannerHarnessUITests": {
                "type": "bundle.ui-testing", "platform": "iOS",
                "sources": [str(repo / "ios/Tests/ScannerUITests.swift")],
                "dependencies": [{"target": "ScannerHarness"}],
                "settings": {"base": {"PRODUCT_BUNDLE_IDENTIFIER": "com.blooper.scanner-harness.tests", "GENERATE_INFOPLIST_FILE": "YES", "SWIFT_VERSION": "5.9"}},
            },
        },
        "schemes": {"ScannerHarness": {"build": {"targets": {"ScannerHarness": "all"}}, "test": {"targets": ["ScannerHarnessUITests"]}}},
    }
    path = folder / "project.yml"
    path.write_text(json.dumps(spec))
    subprocess.run(["xcodegen", "generate", "--spec", str(path)], cwd=folder, check=True)
    subprocess.run([
        "xcodebuild", "test", "-project", str(folder / "ScannerHarness.xcodeproj"),
        "-scheme", "ScannerHarness", "-destination", "platform=iOS Simulator,id=" + sys.argv[1],
        "-derivedDataPath", str(folder / "build"), "CODE_SIGNING_ALLOWED=NO",
    ], check=True)
