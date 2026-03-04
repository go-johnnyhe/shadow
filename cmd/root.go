/*
Copyright © 2025 tianshengqingxiang@gmail.com
*/
package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
)

var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "shadow",
	Short: "Instantly share your code editor with anyone, anywhere - no setup, just shadow start",
	Long: `shadow is a real-time collaborative coding tool for pair programming, code reviews, and live debugging.
Share your code instantly with anyone, no accounts, no setup, no configuration.

How it works:
  1. Start a session:    shadow start .
  2. Share the URL with your partner
  3. They join with:     shadow join '<url>'
  4. Code together in real-time using your favorite editors

No accounts, no servers to manage, just pure collaborative coding.`,
	Run: func(cmd *cobra.Command, args []string) {
		showVersion, _ := cmd.Flags().GetBool("version")
		if showVersion {
			fmt.Println("shadow version:", Version)
			os.Exit(0)
		}

		if err := runInteractiveWizard(); err != nil {
			if !isInteractiveSession() {
				cmd.Help()
				return
			}
			fmt.Printf("Error: %v\n", err)
		}
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().Bool("version", false, "Print the version and exit")
}

func isInteractiveSession() bool {
	stdinInfo, stdinErr := os.Stdin.Stat()
	stdoutInfo, stdoutErr := os.Stdout.Stat()
	if stdinErr != nil || stdoutErr != nil {
		return false
	}
	return (stdinInfo.Mode()&os.ModeCharDevice) != 0 && (stdoutInfo.Mode()&os.ModeCharDevice) != 0
}
