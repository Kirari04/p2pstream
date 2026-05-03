package cmd

import (
	"fmt"
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

		agentToken, _ := cmd.Flags().GetString("agent-token")
		if agentToken == "" {
			agentToken = os.Getenv("AGENT_TOKEN")
		}
		agentID, _ := cmd.Flags().GetString("agent-id")
		if agentID == "" {
			agentID = os.Getenv("AGENT_ID")
		}
		agentName, _ := cmd.Flags().GetString("agent-name")
		if agentName == "" {
			agentName = os.Getenv("AGENT_NAME")
		}
		if agentID == "" {
			fmt.Fprintln(os.Stderr, "agent id required: set --agent-id or AGENT_ID from the management UI setup instructions")
			os.Exit(1)
		}
		if agentToken == "" {
			fmt.Fprintln(os.Stderr, "agent token required: set --agent-token or AGENT_TOKEN from the management UI setup instructions")
			os.Exit(1)
		}

		agent.Run(mgmtURL, agentID, agentName, agentToken)
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.Flags().String("management-url", "", "The HTTP URL of the p2pstream management server")
	agentCmd.Flags().String("agent-token", "", "Bearer token from the management UI setup instructions")
	agentCmd.Flags().String("agent-id", "", "Generated registered agent id from the management UI setup instructions")
	agentCmd.Flags().String("agent-name", "", "Optional agent display name")
}
