package ui

import (
	"os/exec"
	"runtime"
	"strings"
)

// CopyToClipboard copies text to the system clipboard.
// Returns true if successful, false if no clipboard tool is available.
func CopyToClipboard(text string) bool {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return false
		}
	case "windows":
		cmd = exec.Command("clip.exe")
	default:
		return false
	}

	cmd.Stdin = strings.NewReader(text)
	return cmd.Run() == nil
}
