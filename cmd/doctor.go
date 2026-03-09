package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/go-johnnyhe/shadow/internal/runtimehome"
	"github.com/go-johnnyhe/shadow/internal/tunnel"
	"github.com/spf13/cobra"
)

var doctorJSON bool

type doctorReport struct {
	Version           string `json:"version"`
	SupportsJSON      bool   `json:"supports_json"`
	RuntimeHome       string `json:"runtime_home"`
	Platform          string `json:"platform"`
	CloudflaredPath   string `json:"cloudflared_path"`
	CloudflaredExists bool   `json:"cloudflared_exists"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Print machine-friendly runtime information for desktop integrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorJSON {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
		}

		report, err := buildDoctorReport()
		if err != nil {
			if doctorJSON {
				emitJSONError(err.Error())
				return err
			}
			return err
		}

		if doctorJSON {
			data, err := json.Marshal(report)
			if err != nil {
				return err
			}
			data = append(data, '\n')
			_, err = os.Stdout.Write(data)
			return err
		}

		fmt.Printf("Shadow %s\n", report.Version)
		fmt.Printf("Platform: %s\n", report.Platform)
		fmt.Printf("Runtime home: %s\n", report.RuntimeHome)
		fmt.Printf("Supports JSON: %t\n", report.SupportsJSON)
		if report.CloudflaredExists {
			fmt.Printf("cloudflared: %s\n", report.CloudflaredPath)
		} else {
			fmt.Printf("cloudflared: not downloaded yet (expected path: %s)\n", report.CloudflaredPath)
		}
		return nil
	},
}

func buildDoctorReport() (doctorReport, error) {
	runtimeHome, err := runtimehome.Resolve()
	if err != nil {
		return doctorReport{}, err
	}
	cloudflaredPath, err := tunnel.CloudflaredBinaryPath()
	if err != nil {
		return doctorReport{}, err
	}
	_, statErr := os.Stat(cloudflaredPath)

	return doctorReport{
		Version:           Version,
		SupportsJSON:      true,
		RuntimeHome:       runtimeHome,
		Platform:          fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		CloudflaredPath:   cloudflaredPath,
		CloudflaredExists: statErr == nil,
	}, nil
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Emit structured JSON output")
}
