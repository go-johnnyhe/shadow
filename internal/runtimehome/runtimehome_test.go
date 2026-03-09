package runtimehome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesShadowHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(EnvVar, tmpDir)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != tmpDir {
		t.Fatalf("Resolve = %q, want %q", got, tmpDir)
	}
}

func TestResolveFallsBackToHomeShadowDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv(EnvVar, "")
	t.Setenv("HOME", tmpHome)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	want := filepath.Join(tmpHome, ".shadow")
	if got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

func TestEnsureCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "runtime-home")
	t.Setenv(EnvVar, target)

	got, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if got != target {
		t.Fatalf("Ensure = %q, want %q", got, target)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("runtime directory missing: %v", err)
	}
}
