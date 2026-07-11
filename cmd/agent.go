package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"p2pstream/internal/agent"
	"p2pstream/internal/tunnel"
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
		tunnelMaxStreamWindowBytes, err := agentTunnelMaxStreamWindowBytes(cmd)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		tunnelMaxConcurrentRequests, err := agentTunnelMaxConcurrentRequests(cmd)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		allowTargets, _ := cmd.Flags().GetStringArray("allow-target")
		if len(allowTargets) == 0 {
			allowTargets = splitAgentAllowTargets(os.Getenv("AGENT_ALLOW_TARGETS"))
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if err := agent.RunContext(ctx, agent.Options{
			ManagementURL:               mgmtURL,
			PublicID:                    agentID,
			Name:                        agentName,
			Token:                       agentToken,
			ManagementCAFile:            managementCAFile,
			ManagementCAPEMBase64:       managementCAPEMBase64,
			TLSCertFile:                 tlsCertFile,
			TLSKeyFile:                  tlsKeyFile,
			AllowInsecureManagement:     allowInsecureManagement,
			AllowTargets:                allowTargets,
			TunnelMaxStreamWindowBytes:  tunnelMaxStreamWindowBytes,
			TunnelMaxConcurrentRequests: tunnelMaxConcurrentRequests,
		}); err != nil && ctx.Err() == nil {
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
	agentCmd.Flags().Int64("tunnel-max-stream-window-bytes", tunnel.DefaultMaxStreamWindowSizeBytes, "Maximum Yamux receive window per tunnel stream in bytes")
	agentCmd.Flags().Int64("tunnel-max-concurrent-requests", tunnel.DefaultMaxConcurrentAgentRequests, "Maximum concurrent requests served through the agent tunnel")
	agentCmd.Flags().StringArray("allow-target", nil, "Opt-in tunnel destination allowlist entry; repeat for CIDR/IP/hostname with optional port or port range")
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

func agentTunnelMaxStreamWindowBytes(cmd *cobra.Command) (int64, error) {
	if cmd.Flags().Changed("tunnel-max-stream-window-bytes") {
		return cmd.Flags().GetInt64("tunnel-max-stream-window-bytes")
	}
	raw := strings.TrimSpace(os.Getenv("TUNNEL_MAX_STREAM_WINDOW_BYTES"))
	if raw == "" {
		return tunnel.DefaultMaxStreamWindowSizeBytes, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid TUNNEL_MAX_STREAM_WINDOW_BYTES %q: %w", raw, err)
	}
	return value, nil
}

func agentTunnelMaxConcurrentRequests(cmd *cobra.Command) (int64, error) {
	if cmd.Flags().Changed("tunnel-max-concurrent-requests") {
		return cmd.Flags().GetInt64("tunnel-max-concurrent-requests")
	}
	raw := strings.TrimSpace(os.Getenv("TUNNEL_MAX_CONCURRENT_REQUESTS"))
	if raw == "" {
		return tunnel.DefaultMaxConcurrentAgentRequests, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid TUNNEL_MAX_CONCURRENT_REQUESTS %q: %w", raw, err)
	}
	return value, nil
}

func splitAgentAllowTargets(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	targets := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			targets = append(targets, field)
		}
	}
	return targets
}
