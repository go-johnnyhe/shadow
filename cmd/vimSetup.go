/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed extras/autoread.vim
var pluginBody []byte

const luaSnippet = `-- ~/.config/nvim/after/plugin/shadow.lua
vim.opt.autoread = true
vim.opt.updatetime = 100
vim.opt.swapfile = false
local group = vim.api.nvim_create_augroup("shadow_autoread", { clear = true })

-- Timer for file watching
local file_watch_timer = nil

-- Function to check for file changes
local function check_file_changes()
  -- pcall avoids 'checktime' errors in special buffers
  pcall(vim.cmd, "checktime")
end

-- Set up continuous file monitoring with timer
local function setup_file_watcher()
  if file_watch_timer then
    vim.fn.timer_stop(file_watch_timer)
  end
  
  file_watch_timer = vim.fn.timer_start(200, function()
    check_file_changes()
  end, { ['repeat'] = -1 })
end

-- Start the file watcher
setup_file_watcher()

vim.api.nvim_create_autocmd(
  { "FocusGained", "BufEnter", "CursorHold", "CursorHoldI", "TermEnter" },
  {
    group = group,
    pattern = "*",
    callback = check_file_changes,
    desc = "Reload buffer if the file changed on disk",
  }
)

-- Additional autocmd for when vim loses focus but file changes occur
vim.api.nvim_create_autocmd(
  { "FocusLost" },
  {
    group = group,
    pattern = "*",
    callback = function()
      -- Ensure timer continues running even when focus is lost
      if not file_watch_timer then
        setup_file_watcher()
      end
    end,
    desc = "Maintain file watching when focus is lost",
  }
)`

var vimSetupCmd = &cobra.Command{
	Use:   "vimSetup",
	Short: "Set up your vim/nvim to autoreload so your partner's changes instantly appear",
	RunE: func(cmd *cobra.Command, args []string) error {
		failures := []string{}
		var ok bool

		if data, err := nvimDataDir(); err == nil {
			dst := filepath.Join(data,
				"site", "pack", "shadow", "start",
				"autoread", "plugin", "autoread.vim")
			fmt.Println("DEBUG copying to →", dst)
			if err := copyFile(dst, pluginBody); err == nil {
				fmt.Println("✅ Neovim config is done (data path)")
				ok = true
			} else {
				failures = append(failures, "Neovim-data: "+err.Error())
			}
		}

		// nvim config/plugin fallback
		if cfg, err := nvimConfigDir(); err == nil {
			fmt.Println("DEBUG copying to →", cfg)
			if err := installNvimScriptAfterPlugin(cfg); err == nil {
				fmt.Println("✅ Neovim config is done (after/plugin path), restart your nvim")
				ok = true
			} else {
				failures = append(failures, "Neovim-cfg: "+err.Error())
			}
		}

		configDst := filepath.Join(vimSiteDir(),
			"pack", "shadow", "start",
			"autoread", "plugin", "autoread.vim")
		fmt.Println("DEBUG copying to →", configDst)
		if err := copyFile(configDst, pluginBody); err == nil {
			fmt.Println("✅ Vim config is done, restart your vim")
			ok = true
		} else {
			failures = append(failures, "Vim: "+err.Error())
		}

		if !ok {
			return fmt.Errorf("%s", strings.Join(failures, "; "))
		}

		return nil
	},
}

func nvimStdPath(which string) (string, error) {
	cmd := exec.Command(
		"nvim",
		"--headless",
		"-u", "NONE",
		"-c", fmt.Sprintf(`lua print(vim.fn.stdpath("%s"))`, which),
		"-c", "q",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	fmt.Printf("RAW %s output: %q\n", which, out)
	s := strings.TrimSpace(string(out))
	s = strings.TrimSuffix(s, "%")
	return s, nil
}

func nvimDataDir() (string, error) {
	return nvimStdPath("data")
}

func nvimConfigDir() (string, error) {
	return nvimStdPath("config")
}

func installNvimScriptAfterPlugin(cfg string) error {
	dst := filepath.Join(cfg, "after", "plugin", "shadow.lua")
	fmt.Println("DEBUG copying to →", dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(luaSnippet), 0o644)
}

func vimSiteDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vim")
}

func copyFile(dest string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	return os.WriteFile(dest, body, 0o644)
}

func init() {
	rootCmd.AddCommand(vimSetupCmd)
}
