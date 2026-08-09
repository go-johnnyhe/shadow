package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadVerifiedReleaseArchive(t *testing.T) {
	archive := []byte("release archive bytes")
	digest := sha256.Sum256(archive)
	assetName := "shadow_1.2.3_linux_amd64.tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/archive":
			_, _ = response.Write(archive)
		case "/checksums":
			_, _ = response.Write([]byte(hex.EncodeToString(digest[:]) + "  " + assetName + "\n"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	archivePath, err := downloadVerifiedReleaseArchive(server.URL+"/archive", server.URL+"/checksums", assetName, t.TempDir())
	if err != nil {
		t.Fatalf("verified download failed: %v", err)
	}
	defer os.Remove(archivePath)
	got, err := os.ReadFile(archivePath)
	if err != nil || string(got) != string(archive) {
		t.Fatalf("downloaded archive = %q, %v", got, err)
	}
}

func TestDownloadVerifiedReleaseArchiveRejectsMismatchAndCleansUp(t *testing.T) {
	assetName := "shadow_1.2.3_linux_amd64.tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/checksums" {
			_, _ = response.Write([]byte(strings.Repeat("0", 64) + "  " + assetName + "\n"))
			return
		}
		_, _ = response.Write([]byte("tampered archive"))
	}))
	defer server.Close()

	directory := t.TempDir()
	if _, err := downloadVerifiedReleaseArchive(server.URL+"/archive", server.URL+"/checksums", assetName, directory); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".shadow-release-*.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed download left temporary archives: %v", matches)
	}
}

func TestExtractShadowFromTgzAcceptsWindowsBinary(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	want := []byte("windows shadow binary")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "shadow.exe", Mode: 0o755, Size: int64(len(want)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "shadow.exe")
	if err := extractShadowFromTgz(bytes.NewReader(archive.Bytes()), destination); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extracted binary = %q, %v", got, err)
	}
}
