import XCTest

final class ScannerUITests: XCTestCase {
    @MainActor
    func testCancelClosesScannerAndAllowsReopening() {
        let app = XCUIApplication()
        app.launch()
        for _ in 0..<2 {
            app.buttons["Open scanner"].tap()
            let cancel = app.navigationBars["Scan QR code"].buttons["Cancel"]
            XCTAssertTrue(cancel.waitForExistence(timeout: 5))
            cancel.tap()
            XCTAssertTrue(app.buttons["Open scanner"].waitForExistence(timeout: 5))
            XCTAssertTrue(app.staticTexts["Codes received: 0"].exists)
            XCTAssertFalse(app.navigationBars["Scan QR code"].exists)
        }
    }
}
