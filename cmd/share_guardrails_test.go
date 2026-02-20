package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-johnnyhe/shadow/internal/client"
)

func TestValidateShareBaseDirBlocksDangerousPaths(t *testing.T) {
	if err := validateShareBaseDir("/"); err == nil {
		t.Fatalf("expected / to be blocked")
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no usable home directory in this environment")
	}
	if err := validateShareBaseDir(home); err == nil {
		t.Fatalf("expected home directory to be blocked")
	}
}

func TestEstimateShareSnapshotRespectsHardcodedIgnore(t *testing.T) {
	tmp := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("failed to create main file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "node_modules", "lib"), 0755); err != nil {
		t.Fatalf("failed to create node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "node_modules", "lib", "x.js"), []byte("ignored"), 0644); err != nil {
		t.Fatalf("failed to create ignored file: %v", err)
	}

	estimate, err := estimateShareSnapshot(tmp, client.NewOutboundIgnore(tmp))
	if err != nil {
		t.Fatalf("estimate failed: %v", err)
	}
	if estimate.FileCount != 1 {
		t.Fatalf("expected 1 file after ignore filtering, got %d", estimate.FileCount)
	}
}

func TestShouldPromptLargeShareRespectsForce(t *testing.T) {
	estimate := shareSnapshotEstimate{
		FileCount:  largeShareFileCountThreshold + 1,
		TotalBytes: 1,
	}
	if !shouldPromptLargeShare(estimate, false) {
		t.Fatalf("expected prompt when force is disabled")
	}
	if shouldPromptLargeShare(estimate, true) {
		t.Fatalf("expected no prompt when force is enabled")
	}
}
