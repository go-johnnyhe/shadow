package cmd

import (
	"fmt"
	"github.com/charmbracelet/huh"
	"strings"
)

const (
	interactiveActionStart = "start"
	interactiveActionJoin  = "join"
)

func runInteractiveWizard() error {
	var action string
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
	).Run()
	if err != nil {
		return err
	}

	switch action {
	case interactiveActionStart:
		return runInteractiveStart()
	case interactiveActionJoin:
		return runInteractiveJoin()
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

func runInteractiveStart() error {
	const (
		shareCurrentDir = "current_dir"
		shareCustomPath = "custom_path"
	)

	shareChoice := shareCurrentDir
	customPath := ""
	path := "."
	readOnlyJoiners := false

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("What should we share?").
				Options(
					huh.NewOption("Entire current folder (.)", shareCurrentDir),
					huh.NewOption("A specific file or subfolder", shareCustomPath),
				).
				Value(&shareChoice),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("File or subfolder path").
				Placeholder("e.g. main.go or ./src").
				Value(&customPath),
		).WithHideFunc(func() bool { return shareChoice != shareCustomPath }),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Read-only mode for joiners?").
				Value(&readOnlyJoiners),
		),
	).Run()
	if err != nil {
		return err
	}

	if shareChoice == shareCustomPath {
		path = strings.TrimSpace(customPath)
		if path == "" {
			return fmt.Errorf("path cannot be empty for custom share mode")
		}
	} else {
		path = "."
	}

	return runStart(StartOptions{
		Path:            path,
		Port:            startPort,
		ReadOnlyJoiners: readOnlyJoiners,
	})
}

func runInteractiveJoin() error {
	var sessionURL string
	var joinKeyInput string

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Paste the session link").
				Value(&sessionURL),
			huh.NewInput().
				Title("E2E key (optional if link includes #key)").
				Value(&joinKeyInput),
		),
	).Run()
	if err != nil {
		return err
	}

	sessionURL = strings.TrimSpace(sessionURL)
	if sessionURL == "" {
		return fmt.Errorf("session URL cannot be empty")
	}
	return runJoin(JoinOptions{
		SessionURL: sessionURL,
		E2EKey:     strings.TrimSpace(joinKeyInput),
	})
}
