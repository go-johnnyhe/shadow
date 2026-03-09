package tunnel

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-johnnyhe/shadow/internal/runtimehome"
)

func TestCloudflaredBinaryPathUsesShadowHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(runtimehome.EnvVar, tmpDir)

	got, err := CloudflaredBinaryPath()
	if err != nil {
		t.Fatalf("CloudflaredBinaryPath returned error: %v", err)
	}

	binaryName := "cloudflared"
	if runtime.GOOS == "windows" {
		binaryName = "cloudflared.exe"
	}
	want := filepath.Join(tmpDir, binaryName)
	if got != want {
		t.Fatalf("CloudflaredBinaryPath = %q, want %q", got, want)
	}
}
