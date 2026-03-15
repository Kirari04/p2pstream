package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"p2pstream/internal/agent"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Start the p2pstream agent",
	Run: func(cmd *cobra.Command, args []string) {
		mgmtURL, _ := cmd.Flags().GetString("management-url")
		if mgmtURL == "" {
			mgmtURL = os.Getenv("MANAGEMENT_URL")
			if mgmtURL == "" {
				mgmtURL = "http://localhost:8081" // Default to internal mgmt port
			}
		}

		agent.Run(mgmtURL)
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.Flags().String("management-url", "", "The HTTP URL of the p2pstream management server")
}
