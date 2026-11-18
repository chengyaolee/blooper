// A narrow JSON bridge to macOS. No shell, AppleScript, clipboard, or provider keys.
import AppKit
import ApplicationServices
import CoreGraphics
import ImageIO
import ScreenCaptureKit

struct Input: Decodable {
    let action: String
    let x: Int?
    let y: Int?
    let text: String?
    let keys: [String]?
    let direction: String?
    let amount: Int?
    let screen_width: Int?
    let screen_height: Int?
}

enum ControlError: Error, CustomStringConvertible {
    case message(String)
    var description: String { switch self { case let .message(text): return text } }
}

func fail(_ text: String) throws -> Never { throw ControlError.message(text) }

func emit(_ value: [String: Any]) throws {
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject: value))
}

func screenSize(_ bounds: CGRect) -> (Int, Int) {
    let scale = min(1, 1600 / max(bounds.width, bounds.height))
    return (max(1, Int(bounds.width * scale)), max(1, Int(bounds.height * scale)))
}

@available(macOS 14.0, *)
func capture(_ display: CGDirectDisplayID, _ width: Int, _ height: Int) async throws -> [String: Any] {
    let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
    guard let main = content.displays.first(where: { $0.displayID == display }) else { try fail("Main display is unavailable.") }
    let filter = SCContentFilter(display: main, excludingWindows: [])
    let config = SCStreamConfiguration()
    config.width = width; config.height = height; config.showsCursor = true
    let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
    let data = NSMutableData()
    guard let destination = CGImageDestinationCreateWithData(data, "public.jpeg" as CFString, 1, nil) else { try fail("Cannot encode screenshot.") }
    CGImageDestinationAddImage(destination, image, [kCGImageDestinationLossyCompressionQuality: 0.55] as CFDictionary)
    guard CGImageDestinationFinalize(destination), data.length <= 525 * 1024 else { try fail("Screenshot exceeds the transfer limit.") }
    return ["data": (data as Data).base64EncodedString(), "width": width, "height": height]
}

let keyCodes: [String: CGKeyCode] = [
    "A":0,"S":1,"D":2,"F":3,"H":4,"G":5,"Z":6,"X":7,"C":8,"V":9,"B":11,
    "Q":12,"W":13,"E":14,"R":15,"Y":16,"T":17,"1":18,"2":19,"3":20,"4":21,
    "6":22,"5":23,"9":25,"7":26,"8":28,"0":29,"O":31,"U":32,"I":34,"P":35,
    "ENTER":36,"L":37,"J":38,"K":40,"N":45,"M":46,"TAB":48,"SPACE":49,
    "BACKSPACE":51,"ESC":53,"HOME":115,"PAGEUP":116,"DELETE":117,"END":119,
    "PAGEDOWN":121,"LEFT":123,"RIGHT":124,"DOWN":125,"UP":126
]

func run() async throws {
    guard #available(macOS 14.0, *) else { try fail("Computer control requires macOS 14 or newer.") }
    let data = FileHandle.standardInput.readDataToEndOfFile()
    guard data.count < 16384 else { try fail("Input too large.") }
    let input = try JSONDecoder().decode(Input.self, from: data)
    if input.action == "permissions" {
        _ = CGRequestScreenCaptureAccess()
        _ = AXIsProcessTrustedWithOptions([kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary)
        try emit(["output": "Allow blooper in macOS Privacy & Security → Accessibility and Screen & System Audio Recording. Reopen blooper after changing permissions."])
        return
    }
    let session = CGSessionCopyCurrentDictionary() as? [String: Any]
    guard session?["CGSSessionScreenIsLocked"] as? Bool != true,
          session?["kCGSSessionOnConsoleKey"] as? Bool == true else {
        try fail("Unlock this Mac and sign in to its desktop before using computer control.")
    }
    guard CGPreflightScreenCaptureAccess() else { try fail("Screen recording permission is missing. In blooper Permissions, choose Set up Mac permissions.") }
    let display = CGMainDisplayID(), bounds = CGDisplayBounds(CGMainDisplayID())
    let (width, height) = screenSize(bounds)
    guard let source = CGEventSource(stateID: .hidSystemState) else { try fail("Desktop input is unavailable.") }
    if input.action != "screenshot" {
        guard AXIsProcessTrusted() else { try fail("Accessibility permission is missing. In blooper Permissions, choose Set up Mac permissions.") }
        guard input.screen_width == width, input.screen_height == height else {
            try fail("Display size changed since the last screenshot. Take a new screenshot before sending input.")
        }
    }
    switch input.action {
    case "screenshot": break
    case "click", "double_click", "right_click":
        guard let x = input.x, let y = input.y, x >= 0, y >= 0, x < width, y < height else { try fail("Click is outside the latest screenshot.") }
        let point = CGPoint(x: bounds.minX + CGFloat(x) * bounds.width / CGFloat(width),
                            y: bounds.minY + CGFloat(y) * bounds.height / CGFloat(height))
        let right = input.action == "right_click"
        for click in 1...(input.action == "double_click" ? 2 : 1) {
            guard let down = CGEvent(mouseEventSource: source, mouseType: right ? .rightMouseDown : .leftMouseDown, mouseCursorPosition: point, mouseButton: right ? .right : .left),
                  let up = CGEvent(mouseEventSource: source, mouseType: right ? .rightMouseUp : .leftMouseUp, mouseCursorPosition: point, mouseButton: right ? .right : .left) else { try fail("Mouse input is unavailable.") }
            down.setIntegerValueField(.mouseEventClickState, value: Int64(click))
            up.setIntegerValueField(.mouseEventClickState, value: Int64(click))
            down.post(tap: .cghidEventTap); up.post(tap: .cghidEventTap)
        }
    case "type":
        guard let text = input.text, !text.isEmpty, text.count <= 2000 else { try fail("Invalid text.") }
        // Grapheme-sized events preserve surrogate pairs and composed Unicode.
        for character in text {
            let units = Array(String(character).utf16)
            guard units.count <= 20,
                  let down = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: false) else { try fail("Cannot type this text.") }
            down.keyboardSetUnicodeString(stringLength: units.count, unicodeString: units)
            up.keyboardSetUnicodeString(stringLength: units.count, unicodeString: units)
            down.post(tap: .cghidEventTap); up.post(tap: .cghidEventTap)
        }
    case "key":
        guard let keys = input.keys, let last = keys.last, let code = keyCodes[last], keys.count <= 5 else { try fail("Unsupported key.") }
        let modifiers: [String: CGEventFlags] = ["CMD": .maskCommand, "CTRL": .maskControl, "ALT": .maskAlternate, "SHIFT": .maskShift]
        var flags: CGEventFlags = []
        for key in keys.dropLast() {
            guard let flag = modifiers[key] else { try fail("Unsupported modifier.") }
            flags.insert(flag)
        }
        guard let down = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: true),
              let up = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: false) else { try fail("Keyboard input is unavailable.") }
        down.flags = flags; up.flags = flags
        down.post(tap: .cghidEventTap); up.post(tap: .cghidEventTap)
    case "scroll":
        guard let direction = input.direction, let amount = input.amount, (1...1000).contains(amount),
              ["up", "down", "left", "right"].contains(direction) else { try fail("Invalid scroll.") }
        let vertical = direction == "up" ? amount : direction == "down" ? -amount : 0
        let horizontal = direction == "left" ? amount : direction == "right" ? -amount : 0
        guard let event = CGEvent(scrollWheelEvent2Source: source, units: .pixel, wheelCount: 2,
                                  wheel1: Int32(vertical), wheel2: Int32(horizontal), wheel3: 0) else { try fail("Scroll input is unavailable.") }
        event.post(tap: .cghidEventTap)
    default: try fail("Unsupported computer action.")
    }
    if input.action != "screenshot" { try await Task.sleep(nanoseconds: 400_000_000) }
    var result: [String: Any] = ["output": "\(input.action) completed. Main display screenshot: \(width) × \(height) pixels."]
    do { result["image"] = try await capture(display, width, height) }
    catch {
        if input.action == "screenshot" { throw error }
        result["output"] = "Input was delivered, but the next screenshot failed: \(error). Do not repeat the input; take a screenshot to check its effect."
    }
    try emit(result)
}

Task {
    do {
        if CommandLine.arguments.dropFirst().elementsEqual(["--self-test"]) {
            precondition(screenSize(CGRect(x: 0, y: 0, width: 2560, height: 1440)) == (1600, 900))
            precondition(screenSize(CGRect(x: -500, y: 0, width: 900, height: 1600)) == (900, 1600))
            precondition(screenSize(CGRect(x: 0, y: 0, width: 800, height: 600)) == (800, 600))
            precondition(keyCodes["ENTER"] == 36 && keyCodes["A"] == 0)
            try emit(["output": "Computer helper checks passed; no screen capture or input performed."])
        } else { try await run() }
        exit(0)
    }
    catch {
        FileHandle.standardError.write(Data("\(error)\n".utf8))
        exit(1)
    }
}
dispatchMain()
