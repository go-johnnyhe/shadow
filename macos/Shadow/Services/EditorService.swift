import AppKit

/// Detects installed GUI editors and opens directories in them.
enum EditorService {

    struct Editor: Identifiable, Hashable {
        let id: String          // bundle ID or sentinel
        let name: String
        let bundleID: String?   // nil for finder action
    }

    /// Known GUI editors — bundle ID is used to check if installed.
    private static let knownEditors: [(name: String, bundleID: String)] = [
        ("VS Code",        "com.microsoft.VSCode"),
        ("Cursor",         "com.todesktop.230313mzl4w4u92"),
        ("Windsurf",       "com.exafunction.windsurf"),
        ("Zed",            "dev.zed.Zed"),
        ("Kiro",           "dev.kiro.desktop"),
        ("Sublime Text",   "com.sublimetext.4"),
        ("IntelliJ IDEA",  "com.jetbrains.intellij"),
        ("GoLand",         "com.jetbrains.goland"),
        ("WebStorm",       "com.jetbrains.WebStorm"),
        ("PyCharm",        "com.jetbrains.pycharm"),
        ("CLion",          "com.jetbrains.CLion"),
        ("PhpStorm",       "com.jetbrains.PhpStorm"),
        ("RubyMine",       "com.jetbrains.RubyMine"),
        ("Rider",          "com.jetbrains.rider"),
        ("RustRover",      "com.jetbrains.rustrover"),
        ("Nova",           "com.panic.Nova"),
    ]

    /// Known terminal emulators — checked by bundle ID, ordered by preference.
    private static let knownTerminals: [(name: String, bundleID: String)] = [
        ("Ghostty",        "com.mitchellh.ghostty"),
        ("iTerm2",         "com.googlecode.iterm2"),
        ("Kitty",          "net.kovidgoyal.kitty"),
        ("WezTerm",        "org.wezfurlong.wezterm"),
        ("Alacritty",      "org.alacritty"),
        ("Rio",            "com.raphaelamorim.rio"),
        ("Terminal",       "com.apple.Terminal"),
    ]

    /// Returns editors that are currently installed, plus detected terminals and Finder.
    static func availableEditors() -> [Editor] {
        let workspace = NSWorkspace.shared
        var found: [Editor] = []

        for entry in knownEditors {
            if workspace.urlForApplication(withBundleIdentifier: entry.bundleID) != nil {
                found.append(Editor(id: entry.bundleID, name: entry.name, bundleID: entry.bundleID))
            }
        }

        // Add installed terminal emulators
        for entry in knownTerminals {
            if workspace.urlForApplication(withBundleIdentifier: entry.bundleID) != nil {
                found.append(Editor(id: entry.bundleID, name: "New \(entry.name) Window", bundleID: entry.bundleID))
            }
        }

        found.append(Editor(id: "__finder__", name: "Finder", bundleID: nil))
        return found
    }

    /// Open a directory in the selected editor or terminal.
    static func open(_ editor: Editor, directory: String) {
        let url = URL(fileURLWithPath: directory)

        if editor.id == "__finder__" {
            NSWorkspace.shared.open(url)
            return
        }

        guard let bundleID = editor.bundleID,
              let appURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            return
        }

        let config = NSWorkspace.OpenConfiguration()
        NSWorkspace.shared.open([url], withApplicationAt: appURL, configuration: config)
    }
}
