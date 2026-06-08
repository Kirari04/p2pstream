package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "p2pstream",
	Short: "p2pstream is a high-performance reverse proxy toolkit",
	Long:  `A fast and flexible reverse proxy that tunnels incoming traffic through authenticated Yamux agent tunnels.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
