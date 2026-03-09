package tunnel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-johnnyhe/shadow/internal/runtimehome"
	"github.com/go-johnnyhe/shadow/internal/ui"
)

const (
	StatusDownloadingDependency = "downloading_dependency"
	StatusDependencyReady       = "dependency_ready"
)

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

	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}
	reportStatus(reporter, StatusDownloadingDependency, "Downloading cloudflared (~15MB)...")

	var downloadURL string
	var needsExtraction bool
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64"
	case "linux/arm64":
		downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64"
	case "darwin/amd64":
		downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-amd64.tgz"
		needsExtraction = true
	case "darwin/arm64":
		downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-arm64.tgz"
		needsExtraction = true
	case "windows/amd64":
		downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe"
	default:
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// download the binary
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("error downloading cloudflared binary: %v", err)
	}
	defer resp.Body.Close()

	// check status code
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %s", resp.Status)
	}

	if needsExtraction {
		if err := extractCloudflaredFromTgz(resp.Body, binaryPath); err != nil {
			return "", fmt.Errorf("failed to extract the binary %v", err)
		}
	} else {
		// make binary file
		file, err := os.Create(binaryPath)
		if err != nil {
			return "", fmt.Errorf("failed to create file: %v", err)
		}
		defer file.Close()

		// copy file to binary
		_, err = io.Copy(file, resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to copy file: %v", err)
		}
	}

	// on unix systems, make binary into executable
	if runtime.GOOS != "windows" {
		if err := os.Chmod(binaryPath, 0755); err != nil {
			return "", fmt.Errorf("failed to make executable: %v", err)
		}
	}

	reportStatus(reporter, StatusDependencyReady, "cloudflared ready")
	return binaryPath, nil
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

		if strings.HasSuffix(header.Name, "cloudflared") && header.Typeflag == tar.TypeReg {
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
