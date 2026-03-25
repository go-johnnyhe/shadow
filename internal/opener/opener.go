package opener

import (
	"os"
	"os/exec"
	"runtime"
)

// Editor represents a launchable editor or tool.
type Editor struct {
	Name    string // Display name shown in the selector
	Command string // CLI command to invoke
}

// knownEditors lists GUI editors/IDEs detected via PATH lookup.
var knownEditors = []Editor{
	{"VS Code", "code"},
	{"Cursor", "cursor"},
	{"Windsurf", "windsurf"},
	{"Zed", "zed"},
	{"Kiro", "kiro"},
	{"Sublime Text", "subl"},
	{"IntelliJ IDEA", "idea"},
	{"GoLand", "goland"},
	{"WebStorm", "webstorm"},
	{"PyCharm", "pycharm"},
	{"CLion", "clion"},
	{"PhpStorm", "phpstorm"},
	{"RubyMine", "rubymine"},
	{"Rider", "rider"},
	{"RustRover", "rustrover"},
	{"Nova", "nova"},
}

const (
	NameFinder      = "Finder"
	NameFileManager = "File Manager"
	NameSkip        = "Skip"
)

// terminalName returns a display name for the user's current terminal,
// detected via the TERM_PROGRAM environment variable.
func terminalName() string {
	switch os.Getenv("TERM_PROGRAM") {
	case "ghostty":
		return "New Ghostty Window"
	case "iTerm.app":
		return "New iTerm2 Window"
	case "Apple_Terminal":
		return "New Terminal Window"
	case "WezTerm":
		return "New WezTerm Window"
	case "kitty":
		return "New Kitty Window"
	case "alacritty":
		return "New Alacritty Window"
	case "rio":
		return "New Rio Window"
	case "tmux":
		return "New Terminal Window"
	default:
		return "New Terminal Window"
	}
}

// Available returns the editors and tools that are installed, plus
// platform-specific options (terminal, file manager) and a Skip option.
func Available() []Editor {
	var found []Editor
	for _, e := range knownEditors {
		if _, err := exec.LookPath(e.Command); err == nil {
			found = append(found, e)
		}
	}

	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		found = append(found, Editor{terminalName(), "__terminal__"})
	}
	if runtime.GOOS == "darwin" {
		found = append(found, Editor{NameFinder, "__filemanager__"})
	} else if runtime.GOOS == "linux" {
		found = append(found, Editor{NameFileManager, "__filemanager__"})
	}

	found = append(found, Editor{NameSkip, "__skip__"})
	return found
}

// Open launches the selected editor/tool targeting dir in the background.
// It returns immediately without waiting for the process to exit.
func Open(e Editor, dir string) error {
	if e.Command == "__skip__" {
		return nil
	}
	if e.Command == "__terminal__" {
		return openTerminal(dir)
	}
	if e.Command == "__filemanager__" {
		return openFileManager(dir)
	}
	cmd := exec.Command(e.Command, dir)
	return cmd.Start()
}

// macTerminalApps maps TERM_PROGRAM to the macOS app name and optional CLI with args.
var macTerminalApps = map[string]struct {
	app  string   // macOS app name for "open -a"
	cli  string   // CLI binary (empty = no CLI)
	args []string // CLI args before dir
}{
	"ghostty":     {"Ghostty", "ghostty", []string{"--working-directory="}},
	"iTerm.app":   {"iTerm", "", nil},
	"WezTerm":     {"WezTerm", "wezterm", []string{"start", "--cwd"}},
	"kitty":       {"kitty", "kitty", []string{"--directory"}},
	"alacritty":   {"Alacritty", "alacritty", []string{"--working-directory"}},
	"rio":         {"Rio", "rio", []string{"--working-dir"}},
}

func openMacTerminal(termProg, dir string) error {
	info, ok := macTerminalApps[termProg]
	if !ok {
		return exec.Command("open", "-a", "Terminal", dir).Start()
	}
	// Try CLI first if available
	if info.cli != "" {
		if _, err := exec.LookPath(info.cli); err == nil {
			args := make([]string, len(info.args))
			copy(args, info.args)
			// Handle --flag=value style (e.g. ghostty --working-directory=/path)
			if len(args) > 0 && args[len(args)-1][len(args[len(args)-1])-1] == '=' {
				args[len(args)-1] = args[len(args)-1] + dir
			} else {
				args = append(args, dir)
			}
			return exec.Command(info.cli, args...).Start()
		}
	}
	// Fallback to open -a
	return exec.Command("open", "-a", info.app, dir).Start()
}

func openTerminal(dir string) error {
	if runtime.GOOS == "darwin" {
		return openMacTerminal(os.Getenv("TERM_PROGRAM"), dir)
	}
	// Linux: try common terminal emulators
	for _, term := range []string{"ghostty", "rio", "kitty", "wezterm", "alacritty", "gnome-terminal", "konsole", "xfce4-terminal", "xterm"} {
		if _, err := exec.LookPath(term); err == nil {
			switch term {
			case "ghostty":
				return exec.Command(term, "--working-directory="+dir).Start()
			case "kitty":
				return exec.Command(term, "--directory", dir).Start()
			case "wezterm":
				return exec.Command(term, "start", "--cwd", dir).Start()
			case "alacritty":
				return exec.Command(term, "--working-directory", dir).Start()
			case "rio":
				return exec.Command(term, "--working-dir", dir).Start()
			default:
				return exec.Command(term, "--working-directory="+dir).Start()
			}
		}
	}
	return nil
}

func openFileManager(dir string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", dir).Start()
	}
	return exec.Command("xdg-open", dir).Start()
}
