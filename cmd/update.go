package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/go-johnnyhe/shadow/internal/ui"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update shadow to the latest release",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runUpdate(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

type githubRelease struct {
	TagName string `json:"tag_name"`
}

func runUpdate() error {
	fmt.Printf("  %s\n", ui.Dim("checking for updates..."))

	resp, err := http.Get("https://api.github.com/repos/go-johnnyhe/shadow/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to check for updates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to parse release info: %v", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(Version, "v")

	if current != "dev" && current == latest {
		fmt.Printf("  %s\n", ui.Accent("already up to date (v"+latest+")"))
		return nil
	}

	if current == "dev" {
		fmt.Printf("  %s\n", ui.Dim("dev build detected, updating to v"+latest))
	} else {
		fmt.Printf("  %s\n", ui.Dim("updating v"+current+" → v"+latest))
	}

	archiveURL := fmt.Sprintf(
		"https://github.com/go-johnnyhe/shadow/releases/download/v%s/shadow_%s_%s_%s.tar.gz",
		latest, latest, runtime.GOOS, runtime.GOARCH,
	)

	dlResp, err := http.Get(archiveURL)
	if err != nil {
		return fmt.Errorf("failed to download release: %v", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %s", dlResp.Status)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %v", err)
	}

	tmpPath := execPath + ".tmp"

	if err := extractShadowFromTgz(dlResp.Body, tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to extract binary: %v", err)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to set permissions: %v", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied — try running with sudo")
		}
		return fmt.Errorf("failed to replace binary: %v", err)
	}

	fmt.Printf("  %s\n", ui.Accent("updated to v"+latest))
	return nil
}

func extractShadowFromTgz(reader io.Reader, outputPath string) error {
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
			return fmt.Errorf("error reading tar: %v", err)
		}

		if header.Typeflag == tar.TypeReg && (header.Name == "shadow" || strings.HasSuffix(header.Name, "/shadow")) {
			outFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %v", err)
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("failed to write binary: %v", err)
			}
			return nil
		}
	}
	return fmt.Errorf("shadow binary not found in archive")
}
