package cmd

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

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
				mgmtURL = defaultAgentManagementURL()
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

		managementCAFile, _ := cmd.Flags().GetString("management-ca-file")
		if managementCAFile == "" {
			managementCAFile = os.Getenv("MANAGEMENT_CA_FILE")
		}
		managementCAPEMBase64, _ := cmd.Flags().GetString("management-ca-pem-base64")
		if managementCAPEMBase64 == "" {
			managementCAPEMBase64 = os.Getenv("MANAGEMENT_CA_PEM_BASE64")
		}
		tlsCertFile, _ := cmd.Flags().GetString("tls-cert-file")
		if tlsCertFile == "" {
			tlsCertFile = os.Getenv("AGENT_TLS_CERT_FILE")
		}
		tlsKeyFile, _ := cmd.Flags().GetString("tls-key-file")
		if tlsKeyFile == "" {
			tlsKeyFile = os.Getenv("AGENT_TLS_KEY_FILE")
		}
		allowInsecureManagement, _ := cmd.Flags().GetBool("allow-insecure-management")
		if !allowInsecureManagement {
			allowInsecureManagement = envBool("AGENT_ALLOW_INSECURE_MANAGEMENT")
		}

		if err := agent.Run(agent.Options{
			ManagementURL:           mgmtURL,
			PublicID:                agentID,
			Name:                    agentName,
			Token:                   agentToken,
			ManagementCAFile:        managementCAFile,
			ManagementCAPEMBase64:   managementCAPEMBase64,
			TLSCertFile:             tlsCertFile,
			TLSKeyFile:              tlsKeyFile,
			AllowInsecureManagement: allowInsecureManagement,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "agent failed: "+err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.Flags().String("management-url", "", "The HTTPS URL of the p2pstream management server")
	agentCmd.Flags().String("agent-token", "", "Bearer token from the management UI setup instructions")
	agentCmd.Flags().String("agent-id", "", "Generated registered agent id from the management UI setup instructions")
	agentCmd.Flags().String("agent-name", "", "Optional agent display name")
	agentCmd.Flags().String("management-ca-file", "", "PEM CA bundle used to verify the HTTPS management server")
	agentCmd.Flags().String("management-ca-pem-base64", "", "Base64 PEM CA bundle used to verify the HTTPS management server")
	agentCmd.Flags().String("tls-cert-file", "", "PEM client certificate for management mTLS")
	agentCmd.Flags().String("tls-key-file", "", "PEM private key for management mTLS")
	agentCmd.Flags().Bool("allow-insecure-management", false, "Allow an insecure HTTP management URL")
}

func defaultAgentManagementURL() string {
	host := defaultRouteLocalIP()
	if host == "" {
		host = firstNonLoopbackIPv4()
	}
	if host == "" {
		host = "localhost"
	}
	return "https://" + net.JoinHostPort(host, "8081")
}

func defaultRouteLocalIP() string {
	conn, err := net.DialTimeout("udp", "1.1.1.1:443", 500*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() {
		return ""
	}
	return addr.IP.String()
}

func firstNonLoopbackIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				return ipv4.String()
			}
		}
	}
	return ""
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
