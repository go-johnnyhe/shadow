package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-johnnyhe/shadow/internal/runtimehome"
)

func TestBuildDoctorReportUsesShadowHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(runtimehome.EnvVar, tmpDir)

	report, err := buildDoctorReport()
	if err != nil {
		t.Fatalf("buildDoctorReport returned error: %v", err)
	}
	if report.RuntimeHome != tmpDir {
		t.Fatalf("runtime_home = %q, want %q", report.RuntimeHome, tmpDir)
	}
	if report.CloudflaredPath != filepath.Join(tmpDir, "cloudflared") {
		t.Fatalf("cloudflared_path = %q", report.CloudflaredPath)
	}
	if !report.SupportsJSON {
		t.Fatalf("supports_json should be true")
	}
}

func TestDoctorJSONOutputShape(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = origStdout
	})

	report, err := buildDoctorReport()
	if err != nil {
		t.Fatalf("buildDoctorReport returned error: %v", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	_ = w.Close()

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)

	var decoded doctorReport
	if err := json.Unmarshal(buf[:n], &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded.RuntimeHome == "" {
		t.Fatalf("runtime_home should not be empty")
	}
	if decoded.Platform == "" {
		t.Fatalf("platform should not be empty")
	}
}
