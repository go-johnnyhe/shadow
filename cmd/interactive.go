package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/go-johnnyhe/shadow/internal/ui"
)

const (
	interactiveActionStart = "start"
	interactiveActionJoin  = "join"
)

func shadowTheme() *huh.Theme {
	t := huh.ThemeCharm()
	cyan := lipgloss.Color("#36CFC9")
	white := lipgloss.Color("#FFFDF5")

	// Titles: clean white instead of purple
	t.Focused.Title = t.Focused.Title.Foreground(white)

	// All interactive accents: cyan instead of fuchsia
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(cyan)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(cyan)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(cyan)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(cyan)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Background(cyan)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(cyan)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(cyan)

	// Selected items: cyan instead of green
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(cyan)
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(cyan)

	return t
}

func showFirstRunWelcome() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sentinel := filepath.Join(homeDir, ".shadow", ".welcome")
	if _, err := os.Stat(sentinel); err == nil {
		return
	}
	// Ensure ~/.shadow/ exists (may already from cloudflared setup)
	os.MkdirAll(filepath.Join(homeDir, ".shadow"), 0755)

	fmt.Println()
	fmt.Println("  Welcome to shadow!")
	fmt.Println()
	fmt.Println("  Pair programming is frustrating when everyone has to use the same")
	fmt.Println("  editor. Shadow lets each person use their own: vim, neovim, VS Code,")
	fmt.Println("  JetBrains, Emacs, Jupyter, you name it.")
	fmt.Println()
	fmt.Println("  It syncs file changes in real time through an encrypted tunnel")
	fmt.Println("  powered by cloudflared. Your code never touches the wire unencrypted.")
	fmt.Println()
	fmt.Println("  To get started, cd into the project you want to share and run: shadow")
	fmt.Println()

	os.WriteFile(sentinel, []byte{}, 0644)
}

func runInteractiveWizard() error {
	showFirstRunWelcome()
	fmt.Printf("\n  %s\n  %s\n\n", ui.Bold("◗ shadow"), ui.Dim("real-time code sharing, no accounts, no setup"))

	var action string
	var readOnlyJoiners bool
	var sessionURL string

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("I want to").
				Options(
					huh.NewOption("start sharing", interactiveActionStart),
					huh.NewOption("join a session", interactiveActionJoin),
				).
				Value(&action),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Make joiners read-only?").
				Description("They'll see your changes but their edits won't sync back.").
				Affirmative("Yes").
				Negative("No").
				Value(&readOnlyJoiners),
		).WithHideFunc(func() bool { return action != interactiveActionStart }),
		huh.NewGroup(
			huh.NewInput().
				Title("Paste the shadow join command or URL").
				Value(&sessionURL),
		).WithHideFunc(func() bool { return action != interactiveActionJoin }),
	).WithTheme(shadowTheme()).WithKeyMap(func() *huh.KeyMap {
		km := huh.NewDefaultKeyMap()
		km.Select.Filter.SetEnabled(false)
		return km
	}()).Run()
	if err != nil {
		return err
	}

	switch action {
	case interactiveActionStart:
		return runStart(StartOptions{
			Path:            ".",
			Port:            startPort,
			ReadOnlyJoiners: readOnlyJoiners,
		})
	case interactiveActionJoin:
		sessionURL = strings.TrimSpace(sessionURL)
		// Handle pasted "shadow join '<url>'" commands
		sessionURL = strings.TrimPrefix(sessionURL, "shadow join ")
		sessionURL = strings.Trim(sessionURL, "'\"")
		sessionURL = strings.TrimSpace(sessionURL)
		if sessionURL == "" {
			return fmt.Errorf("session URL cannot be empty")
		}
		return runJoin(JoinOptions{
			SessionURL: sessionURL,
		})
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}
