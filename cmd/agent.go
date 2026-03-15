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
		serverURL, _ := cmd.Flags().GetString("server-url")
		if serverURL == "" {
			serverURL = os.Getenv("SERVER_URL")
			if serverURL == "" {
				serverURL = "ws://localhost:8080/ws"
			}
		}

		apiStatsURL, _ := cmd.Flags().GetString("stats-url")
		if apiStatsURL == "" {
			apiStatsURL = os.Getenv("SERVER_STATS_URL")
			if apiStatsURL == "" {
				apiStatsURL = "http://localhost:8080/api/agent/stats"
			}
		}

		agent.Run(serverURL, apiStatsURL)
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.Flags().String("server-url", "", "The WebSocket URL of the p2pstream server")
	agentCmd.Flags().String("stats-url", "", "The HTTP URL for reporting agent stats")
}
