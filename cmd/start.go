package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var startPort int
var startReadOnlyJoiners bool
var startKey string
var startPathFlag string
var startForce bool

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start <file-or-directory>",
	Short: "Start a collaborative coding session and share files",
	Long: `Start a new collaborative coding session with instant file sharing.

This command will:
- Launch a WebSocket server for real-time collaboration
- Create a secure tunnel using Cloudflared (no setup required)
- Share the current directory or specified files with anyone who joins the session
- Generate a shareable URL for your coding partner

Example:
  shadow start main.py              # Share a single file
  shadow start .                    # Share current directory

The generated URL can be shared with anyone - they can join using:
  shadow join '<your-session-url>#<e2e-key>'

Perfect for mock interviews, pair programming, and collaborative debugging.`,
	Run: func(cmd *cobra.Command, args []string) {
		targetPath, err := resolveStartPath(args, startPathFlag)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			cmd.Usage()
			return
		}

		err = runStart(StartOptions{
			Path:            targetPath,
			Port:            startPort,
			E2EKey:          startKey,
			ReadOnlyJoiners: startReadOnlyJoiners,
			Force:           startForce,
		})
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	},
}

func resolveStartPath(args []string, pathFlag string) (string, error) {
	hasArg := len(args) == 1
	hasFlag := pathFlag != ""

	if len(args) > 1 {
		return "", fmt.Errorf("this command accepts at most one positional path")
	}
	if hasArg && hasFlag {
		return "", fmt.Errorf("choose either positional path or --path, not both")
	}
	if hasArg {
		return args[0], nil
	}
	if hasFlag {
		return pathFlag, nil
	}
	return ".", nil
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().IntVarP(&startPort, "port", "p", 8080, "Port to run the server on (will auto-increment if in use)")
	startCmd.Flags().BoolVar(&startReadOnlyJoiners, "read-only-joiners", false, "Allow joiners to view updates without uploading local edits")
	startCmd.Flags().StringVar(&startKey, "key", "", "E2E share key (auto-generated if empty)")
	startCmd.Flags().StringVar(&startPathFlag, "path", "", "Path to share (alternative to positional argument)")
	startCmd.Flags().BoolVar(&startForce, "force", false, "Bypass large-directory warning and continue")
}
