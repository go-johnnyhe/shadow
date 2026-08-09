package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestInstallCloudflaredVerifiesDownloadBeforeReplace(t *testing.T) {
	binary := []byte("verified cloudflared binary")
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(binary)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "cloudflared")
	asset := cloudflaredAsset{name: "cloudflared", downloadSHA: hex.EncodeToString(digest[:]), binarySHA: hex.EncodeToString(digest[:])}
	if err := installCloudflared(server.URL, asset, destination); err != nil {
		t.Fatalf("verified install failed: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("installed binary = %q, %v", got, err)
	}
	if !cloudflaredCacheVerified(destination, asset) {
		t.Fatal("installed binary did not pass cache verification")
	}

	if err := os.WriteFile(destination, []byte("existing safe binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	badAsset := asset
	badAsset.downloadSHA = strings.Repeat("0", 64)
	if err := installCloudflared(server.URL, badAsset, destination); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
	got, err = os.ReadFile(destination)
	if err != nil || string(got) != "existing safe binary" {
		t.Fatalf("failed verification replaced existing binary: %q, %v", got, err)
	}
}

func TestCloudflaredCacheRejectsTamperedBinary(t *testing.T) {
	want := []byte("expected")
	digest := sha256.Sum256(want)
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := cloudflaredAsset{binarySHA: hex.EncodeToString(digest[:]), extractTGZ: true}
	if cloudflaredCacheVerified(path, asset) {
		t.Fatal("tampered cached binary passed verification")
	}
}

func TestReplaceFileRollbackKeepsPreviousBinary(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "new-cloudflared")
	destination := filepath.Join(directory, "cloudflared")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	renames := 0
	err := replaceFileWithBackup(source, destination, func(from, to string) error {
		renames++
		if renames == 2 {
			return errors.New("injected replacement failure")
		}
		return os.Rename(from, to)
	})
	if err == nil {
		t.Fatal("injected replacement failure was ignored")
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("previous binary was not restored: %q, %v", got, readErr)
	}
	got, readErr = os.ReadFile(source)
	if readErr != nil || string(got) != "new" {
		t.Fatalf("new binary was not preserved after rollback: %q, %v", got, readErr)
	}
}
