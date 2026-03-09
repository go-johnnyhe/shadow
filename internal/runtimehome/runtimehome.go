package runtimehome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const EnvVar = "SHADOW_HOME"

// Resolve returns the writable runtime directory Shadow should use for
// downloaded helpers and other app-scoped state.
func Resolve() (string, error) {
	override := strings.TrimSpace(os.Getenv(EnvVar))
	if override != "" {
		if filepath.IsAbs(override) {
			return filepath.Clean(override), nil
		}
		absPath, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("failed to resolve %s: %w", EnvVar, err)
		}
		return filepath.Clean(absPath), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".shadow"), nil
}

// Ensure creates the runtime directory if it does not already exist.
func Ensure() (string, error) {
	dir, err := Resolve()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create runtime directory: %w", err)
	}
	return dir, nil
}

// Join appends path segments onto the resolved runtime directory.
func Join(parts ...string) (string, error) {
	baseDir, err := Resolve()
	if err != nil {
		return "", err
	}
	pathParts := append([]string{baseDir}, parts...)
	return filepath.Join(pathParts...), nil
}
