package tunnel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/go-johnnyhe/shadow/internal/runtimehome"
	"github.com/go-johnnyhe/shadow/internal/ui"
)

const (
	StatusDownloadingDependency = "downloading_dependency"
	StatusDependencyReady       = "dependency_ready"
	cloudflaredVersion          = "2026.7.3"
	maxCloudflaredAssetBytes    = 100 * 1024 * 1024
)

type cloudflaredAsset struct {
	name        string
	downloadSHA string
	binarySHA   string
	extractTGZ  bool
}

var cloudflaredAssets = map[string]cloudflaredAsset{
	"darwin/amd64":  {name: "cloudflared-darwin-amd64.tgz", downloadSHA: "70d1c8684fa6d14b5843787ec8d1ea8e18b23650e424f4ea43d849a506487c3b", binarySHA: "e88fe5874d42a94f49a7ea59cabc3722d2962d0449232b0f3b1a426a712e275c", extractTGZ: true},
	"darwin/arm64":  {name: "cloudflared-darwin-arm64.tgz", downloadSHA: "90c5a4f914d705fd70c135dba6d80b1791d254b08d6d4136301941f88330dd09", binarySHA: "f35c50089cd25f77a4cb5a2152036bc26db15aa31fbe11f7995d2e42a4ed6257", extractTGZ: true},
	"linux/amd64":   {name: "cloudflared-linux-amd64", downloadSHA: "9d71c677db00134c1bd4144b7783486b654ad281b1ea62b4972098d19f770f17", binarySHA: "9d71c677db00134c1bd4144b7783486b654ad281b1ea62b4972098d19f770f17"},
	"linux/arm64":   {name: "cloudflared-linux-arm64", downloadSHA: "65259e652a7bea08bf5df603233ab22b8bf3116af8df9f9206209af6a1b955c0", binarySHA: "65259e652a7bea08bf5df603233ab22b8bf3116af8df9f9206209af6a1b955c0"},
	"windows/amd64": {name: "cloudflared-windows-amd64.exe", downloadSHA: "8635da433b6df8194746e88ed9d2589566c20e38bfc2a80e431a348b7c765841", binarySHA: "8635da433b6df8194746e88ed9d2589566c20e38bfc2a80e431a348b7c765841"},
}

var cloudflaredDownloadHTTPClient = &http.Client{Timeout: 2 * time.Minute}

type StatusReporter func(event, message string)

func reportStatus(reporter StatusReporter, event, message string) {
	if reporter != nil {
		reporter(event, message)
		return
	}
	fmt.Printf("  %s\n", ui.Dim(message))
}

func CloudflaredBinaryPath() (string, error) {
	binaryName := "cloudflared"
	if runtime.GOOS == "windows" {
		binaryName = "cloudflared.exe"
	}

	runtimeDir, err := runtimehome.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(runtimeDir, binaryName), nil
}

func getCloudflaredBinary(reporter StatusReporter) (string, error) {
	if _, err := runtimehome.Ensure(); err != nil {
		return "", err
	}

	binaryPath, err := CloudflaredBinaryPath()
	if err != nil {
		return "", err
	}

	asset, ok := cloudflaredAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if cloudflaredCacheVerified(binaryPath, asset) {
		return binaryPath, nil
	}
	reportStatus(reporter, StatusDownloadingDependency, "Downloading cloudflared (~15MB)...")

	downloadURL := fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/%s", cloudflaredVersion, asset.name)
	if err := installCloudflared(downloadURL, asset, binaryPath); err != nil {
		return "", err
	}

	reportStatus(reporter, StatusDependencyReady, "cloudflared ready")
	return binaryPath, nil
}

func cloudflaredCacheVerified(binaryPath string, asset cloudflaredAsset) bool {
	file, err := os.Open(binaryPath)
	if err != nil {
		return false
	}
	defer file.Close()
	return verifySHA256(file, asset.binarySHA) == nil
}

func installCloudflared(downloadURL string, asset cloudflaredAsset, binaryPath string) error {
	resp, err := cloudflaredDownloadHTTPClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("error downloading cloudflared binary: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %s", resp.Status)
	}

	download, err := os.CreateTemp(filepath.Dir(binaryPath), ".cloudflared-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary download: %v", err)
	}
	downloadPath := download.Name()
	defer os.Remove(downloadPath)
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(download, hasher), io.LimitReader(resp.Body, maxCloudflaredAssetBytes+1))
	closeErr := download.Close()
	if copyErr != nil {
		return fmt.Errorf("failed to save cloudflared download: %v", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close cloudflared download: %v", closeErr)
	}
	if written > maxCloudflaredAssetBytes {
		return fmt.Errorf("cloudflared download exceeds size limit")
	}
	if err := compareSHA256(hasher.Sum(nil), asset.downloadSHA); err != nil {
		return fmt.Errorf("cloudflared checksum verification failed: %v", err)
	}

	installPath := downloadPath
	if asset.extractTGZ {
		extracted, err := os.CreateTemp(filepath.Dir(binaryPath), ".cloudflared-binary-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary binary: %v", err)
		}
		installPath = extracted.Name()
		if err := extracted.Close(); err != nil {
			return err
		}
		defer os.Remove(installPath)
		archive, err := os.Open(downloadPath)
		if err != nil {
			return err
		}
		err = extractCloudflaredFromTgz(archive, installPath)
		_ = archive.Close()
		if err != nil {
			return fmt.Errorf("failed to extract the binary: %v", err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(installPath, 0o755); err != nil {
			return fmt.Errorf("failed to make executable: %v", err)
		}
	}
	installed, err := os.Open(installPath)
	if err != nil {
		return err
	}
	verifyErr := verifySHA256(installed, asset.binarySHA)
	_ = installed.Close()
	if verifyErr != nil {
		return fmt.Errorf("cloudflared binary checksum verification failed: %v", verifyErr)
	}
	if err := replaceFile(installPath, binaryPath); err != nil {
		return fmt.Errorf("failed to install cloudflared: %v", err)
	}
	return nil
}

func verifySHA256(reader io.Reader, expected string) error {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return err
	}
	return compareSHA256(hasher.Sum(nil), expected)
}

func compareSHA256(actual []byte, expectedHex string) error {
	expected, err := hex.DecodeString(expectedHex)
	if err != nil || len(expected) != sha256.Size {
		return fmt.Errorf("invalid expected SHA-256")
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}

func replaceFile(source, destination string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(source, destination)
	}
	return replaceFileWithBackup(source, destination, os.Rename)
}

func replaceFileWithBackup(source, destination string, rename func(string, string) error) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return rename(source, destination)
	} else if err != nil {
		return err
	}

	backup, err := os.CreateTemp(filepath.Dir(destination), ".cloudflared-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}

	if err := rename(destination, backupPath); err != nil {
		return err
	}
	if err := rename(source, destination); err != nil {
		if rollbackErr := rename(backupPath, destination); rollbackErr != nil {
			return fmt.Errorf("replace failed: %v; rollback failed; previous binary remains at %s: %v", err, backupPath, rollbackErr)
		}
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func extractCloudflaredFromTgz(reader io.Reader, outputPath string) error {
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()
	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading from tar header: %v", err)
		}

		if path.Base(header.Name) == "cloudflared" && header.Typeflag == tar.TypeReg {
			if header.Size < 0 || header.Size > maxCloudflaredAssetBytes {
				return fmt.Errorf("cloudflared binary exceeds size limit")
			}
			outFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %v", err)
			}
			defer outFile.Close()
			_, err = io.Copy(outFile, tarReader)
			if err != nil {
				return fmt.Errorf("error copying binary to output file: %v", err)
			}
			return nil
		}
	}
	return fmt.Errorf("cloudflared binary not found in the downloaded archive")
}

func StartCloudflaredTunnel(ctx context.Context, port int, reporter StatusReporter) (string, error) {
	binary, err := getCloudflaredBinary(reporter)
	if err != nil {
		return "", fmt.Errorf("error getting cloudflared binary: %v", err)
	}

	cmd := exec.CommandContext(ctx, binary, "tunnel", "--url", fmt.Sprintf("localhost:%d", port))
	// stdout, err := cmd.StdoutPipe()
	// if err != nil {
	// 	return "", fmt.Errorf("failed to create pipe: %v", err)
	// }
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start the command: %v", err)
	}

	// go io.Copy(os.Stderr, stderr)
	// reader, writer := io.Pipe()
	// go func() {
	// 	defer writer.Close()
	// 	io.Copy(io.MultiWriter(os.Stderr, writer), stderr)
	// }()
	scanner := bufio.NewScanner(stderr)
	urlRegex := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.[a-z]+`)

	timeout := time.After(45 * time.Second)
	urlChan := make(chan string, 1)

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if match := urlRegex.FindString(line); match != "" {
				urlChan <- match
				return
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "cloudflare scan error: %v\n", err)
		}
	}()

	select {
	case url := <-urlChan:
		return url, nil
	case <-timeout:
		cmd.Process.Kill()
		cmd.Wait()
		return "", fmt.Errorf("timeout waiting for tunnel URL (45s)")
	}

}
