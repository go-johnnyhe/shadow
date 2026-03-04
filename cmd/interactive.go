package cmd

import (
	"fmt"
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

func runInteractiveWizard() error {
	fmt.Printf("\n  %s\n  %s\n\n", ui.Bold("◗ shadow"), ui.Dim("real-time code sharing — no accounts, no setup"))

	var action string
	var readOnlyJoiners bool
	var sessionURL string

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("What do you want to do?").
				Options(
					huh.NewOption("Start sharing session", interactiveActionStart),
					huh.NewOption("Join sharing session", interactiveActionJoin),
				).
				Value(&action),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Read-only mode for joiners?").
				Value(&readOnlyJoiners),
		).WithHideFunc(func() bool { return action != interactiveActionStart }),
		huh.NewGroup(
			huh.NewInput().
				Title("Paste the shadow join command or URL").
				Value(&sessionURL),
		).WithHideFunc(func() bool { return action != interactiveActionJoin }),
	).WithTheme(shadowTheme()).Run()
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
