package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

type mcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type vscodeMCPServerEntry struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type aiTool struct {
	Name       string
	ConfigPath string
	Format     string // "json", "toml", "vscode", or "zed"
}

var mcpInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Shadow MCP config for AI agents",
	Long: `Automatically configure Shadow as an MCP server for supported AI agents.

Detects installed AI tools and writes the MCP config so they can use
shadow_start, shadow_join, shadow_status, and shadow_stop.

Supported tools:
  - Claude Code  (~/.claude/mcp.json)
  - Cursor       (~/.cursor/mcp.json)
  - Windsurf     (~/.codeium/windsurf/mcp_config.json)
  - VS Code      (User profile mcp.json)
  - Zed          (~/.config/zed/settings.json)
  - Kiro         (~/.kiro/settings/mcp.json)
  - Codex        (~/.codex/config.toml)
  - Cline        (VS Code globalStorage)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMCPInstall()
	},
}

func init() {
	mcpCmd.AddCommand(mcpInstallCmd)
}

func runMCPInstall() error {
	shadowCmd := resolveShadowCommand()

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	tools := []aiTool{
		{Name: "Claude Code", ConfigPath: filepath.Join(home, ".claude", "mcp.json"), Format: "json"},
		{Name: "Cursor", ConfigPath: filepath.Join(home, ".cursor", "mcp.json"), Format: "json"},
		{Name: "Windsurf", ConfigPath: filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), Format: "json"},
		{Name: "VS Code", ConfigPath: vscodeConfigPath(home), Format: "vscode"},
		{Name: "Zed", ConfigPath: filepath.Join(home, ".config", "zed", "settings.json"), Format: "zed"},
		{Name: "Kiro", ConfigPath: filepath.Join(home, ".kiro", "settings", "mcp.json"), Format: "json"},
		{Name: "Codex", ConfigPath: filepath.Join(home, ".codex", "config.toml"), Format: "toml"},
		{Name: "Cline", ConfigPath: clineConfigPath(home), Format: "json"},
	}

	installed := 0
	for _, tool := range tools {
		dir := filepath.Dir(tool.ConfigPath)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Printf("  skipped %s (%s not found)\n", tool.Name, dir)
			continue
		}

		var err error
		switch tool.Format {
		case "toml":
			err = writeCodexMCPConfig(tool.ConfigPath, shadowCmd)
		case "vscode":
			err = writeVSCodeMCPConfig(tool.ConfigPath, shadowCmd)
		case "zed":
			err = writeZedMCPConfig(tool.ConfigPath, shadowCmd)
		default:
			err = writeMCPConfig(tool.ConfigPath, shadowCmd)
		}
		if err != nil {
			fmt.Printf("  failed  %s: %v\n", tool.Name, err)
			continue
		}

		fmt.Printf("  installed for %s → %s\n", tool.Name, tool.ConfigPath)
		installed++
	}

	if installed == 0 {
		fmt.Println("\nNo AI tools detected. You can manually add to your MCP config:")
		fmt.Printf("  {\"mcpServers\": {\"shadow\": {\"command\": %q, \"args\": [\"mcp\"]}}}\n", shadowCmd)
	} else {
		fmt.Println("\n  Restart your AI agent to pick up the changes.")
	}

	return nil
}

// resolveShadowCommand returns "shadow" if the current binary is on PATH,
// otherwise returns the absolute path to the binary.
func resolveShadowCommand() string {
	pathBin, err := exec.LookPath("shadow")
	if err == nil {
		exe, exeErr := os.Executable()
		if exeErr == nil {
			exeResolved, _ := filepath.EvalSymlinks(exe)
			pathResolved, _ := filepath.EvalSymlinks(pathBin)
			if exeResolved == pathResolved {
				return "shadow"
			}
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return "shadow"
	}
	return exe
}

// vscodeConfigPath returns the platform-specific path to VS Code's user-level mcp.json.
func vscodeConfigPath(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")
	case "linux":
		return filepath.Join(home, ".config", "Code", "User", "mcp.json")
	default: // windows
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Code", "User", "mcp.json")
	}
}

// clineConfigPath returns the platform-specific path to Cline's MCP settings file.
func clineConfigPath(home string) string {
	const relPath = "Code/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json"
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", relPath)
	case "linux":
		return filepath.Join(home, ".config", relPath)
	default: // windows
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, relPath)
	}
}

// writeVSCodeMCPConfig writes VS Code's mcp.json format which uses "servers"
// (not "mcpServers") and requires a "type" field.
func writeVSCodeMCPConfig(configPath, shadowCmd string) error {
	config := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse existing config: %w", err)
		}
	}

	servers, ok := config["servers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	servers["shadow"] = vscodeMCPServerEntry{
		Type:    "stdio",
		Command: shadowCmd,
		Args:    []string{"mcp"},
	}
	config["servers"] = servers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	out = append(out, '\n')

	return os.WriteFile(configPath, out, 0644)
}

// writeZedMCPConfig merges into Zed's settings.json under "context_servers".
// Zed uses JSONC (JSON with comments), so we strip comments before parsing
// and preserve them when writing back.
func writeZedMCPConfig(configPath, shadowCmd string) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Strip single-line comments for JSON parsing.
	stripped := stripJSONComments(content)

	config := make(map[string]any)
	if len(strings.TrimSpace(stripped)) > 0 {
		if err := json.Unmarshal([]byte(stripped), &config); err != nil {
			return fmt.Errorf("failed to parse existing config: %w", err)
		}
	}

	servers, ok := config["context_servers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	servers["shadow"] = mcpServerEntry{
		Command: shadowCmd,
		Args:    []string{"mcp"},
	}
	config["context_servers"] = servers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	out = append(out, '\n')

	return os.WriteFile(configPath, out, 0644)
}

// stripJSONComments removes single-line // comments from JSONC content,
// being careful not to strip // inside quoted strings.
func stripJSONComments(s string) string {
	var b strings.Builder
	inString := false
	i := 0
	for i < len(s) {
		if inString {
			if s[i] == '\\' && i+1 < len(s) {
				b.WriteByte(s[i])
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if s[i] == '"' {
				inString = false
			}
			b.WriteByte(s[i])
			i++
		} else {
			if s[i] == '"' {
				inString = true
				b.WriteByte(s[i])
				i++
			} else if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
				// Skip to end of line.
				for i < len(s) && s[i] != '\n' {
					i++
				}
			} else {
				b.WriteByte(s[i])
				i++
			}
		}
	}
	return b.String()
}

// writeCodexMCPConfig adds/updates [mcp_servers.shadow] in Codex's config.toml.
// Uses simple string manipulation to avoid pulling in a TOML library.
func writeCodexMCPConfig(configPath, shadowCmd string) error {
	serverBlock := fmt.Sprintf("[mcp_servers.shadow]\ncommand = %q\nargs = [\"mcp\"]\n", shadowCmd)

	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Check if [mcp_servers.shadow] already exists — replace it.
	const marker = "[mcp_servers.shadow]"
	if idx := strings.Index(content, marker); idx != -1 {
		// Find the end of this section: next [section] header or EOF.
		rest := content[idx+len(marker):]
		endIdx := strings.Index(rest, "\n[")
		if endIdx == -1 {
			// Section goes to EOF.
			content = content[:idx] + serverBlock
		} else {
			content = content[:idx] + serverBlock + rest[endIdx+1:]
		}
	} else {
		// Append the new section.
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + serverBlock
	}

	return os.WriteFile(configPath, []byte(content), 0600)
}

// writeMCPConfig reads an existing MCP config (if any), adds/updates the
// "shadow" server entry, and writes it back — preserving other servers.
func writeMCPConfig(configPath, shadowCmd string) error {
	config := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse existing config: %w", err)
		}
	}

	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	servers["shadow"] = mcpServerEntry{
		Command: shadowCmd,
		Args:    []string{"mcp"},
	}
	config["mcpServers"] = servers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	out = append(out, '\n')

	return os.WriteFile(configPath, out, 0644)
}
