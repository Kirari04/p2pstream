package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"

	"p2pstream/internal/buildinfo"
	"p2pstream/internal/releaseversion"
	"p2pstream/internal/tunnel"
)

const (
	DefaultConfigDir = "p2pstream-data"

	databaseFileName = "p2pstream.db"
	certsDirName     = "certs"
)

type Config struct {
	ManagementPort                    string `env:"MANAGEMENT_PORT" envDefault:"8081"`
	ManagementBindAddress             string `env:"MANAGEMENT_BIND_ADDRESS" envDefault:"0.0.0.0"`
	ConfigDir                         string `env:"CONFIG_DIR" envDefault:"p2pstream-data"`
	DatabaseURL                       string `env:"DATABASE_URL"`
	Env                               string `env:"ENV" envDefault:"development"` // development or production
	ManagementUIDisabled              bool   `env:"MANAGEMENT_UI_DISABLED" envDefault:"false"`
	ManagementUIDevProxy              string `env:"MANAGEMENT_UI_DEV_PROXY"`
	ManagementUIDistDir               string `env:"MANAGEMENT_UI_DIST_DIR" envDefault:"web/management/dist"`
	ManagementCookieSecure            bool   `env:"MANAGEMENT_COOKIE_SECURE" envDefault:"false"`
	ManagementTLSCertFile             string `env:"MANAGEMENT_TLS_CERT_FILE"`
	ManagementTLSKeyFile              string `env:"MANAGEMENT_TLS_KEY_FILE"`
	ManagementTLSClientCAFile         string `env:"MANAGEMENT_TLS_CLIENT_CA_FILE"`
	ManagementTLSMode                 string `env:"MANAGEMENT_TLS_MODE" envDefault:"auto"`
	ManagementAllowInsecureHTTP       bool   `env:"MANAGEMENT_ALLOW_INSECURE_HTTP" envDefault:"false"`
	ManagementPublicURL               string `env:"MANAGEMENT_PUBLIC_URL"`
	ManagementSetupToken              string `env:"MANAGEMENT_SETUP_TOKEN"`
	ManagementTrustedProxyCIDRs       string `env:"MANAGEMENT_TRUSTED_PROXY_CIDRS"`
	ManagementClientIPHeader          string `env:"MANAGEMENT_CLIENT_IP_HEADER" envDefault:"X-Forwarded-For"`
	ManagementClientIPMode            string `env:"MANAGEMENT_CLIENT_IP_MODE" envDefault:"trusted_chain"`
	ManagementAdvertiseHost           string `env:"MANAGEMENT_ADVERTISE_HOST"`
	ManagementTLSExtraHosts           string `env:"MANAGEMENT_TLS_EXTRA_HOSTS"`
	AgentUpdatesEnabled               bool   `env:"AGENT_UPDATES_ENABLED" envDefault:"false"`
	AgentUpdateRepository             string `env:"AGENT_UPDATE_REPOSITORY" envDefault:"Kirari04/p2pstream"`
	AgentUpdateChannel                string `env:"AGENT_UPDATE_CHANNEL"`
	AgentUpdateAuthorityKeyFile       string `env:"AGENT_UPDATE_AUTHORITY_KEY_FILE"`
	AgentUpdateCatalogRefreshMillis   int64  `env:"AGENT_UPDATE_CATALOG_REFRESH_MILLIS" envDefault:"300000"`
	AgentUpdateHTTPTimeoutMillis      int64  `env:"AGENT_UPDATE_HTTP_TIMEOUT_MILLIS" envDefault:"15000"`
	PublicCacheDir                    string `env:"PUBLIC_CACHE_DIR"`
	PublicMaxHeaderBytes              int    `env:"PUBLIC_MAX_HEADER_BYTES" envDefault:"65536"`
	PublicMaxRequestBodyBytes         int64  `env:"PUBLIC_MAX_REQUEST_BODY_BYTES" envDefault:"1073741824"`
	PublicRequestBodyIdleMillis       int64  `env:"PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS" envDefault:"30000"`
	PublicMaxConcurrentRequests       int64  `env:"PUBLIC_MAX_CONCURRENT_REQUESTS" envDefault:"2048"`
	PublicMaxConcurrentPerTarget      int64  `env:"PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET" envDefault:"0"`
	PublicMaxConcurrentPerClient      int64  `env:"PUBLIC_MAX_CONCURRENT_REQUESTS_PER_CLIENT" envDefault:"512"`
	PublicMaxConcurrentConnections    int64  `env:"PUBLIC_MAX_CONCURRENT_CONNECTIONS" envDefault:"0"`
	PublicMaxConnectionsPerPeer       int64  `env:"PUBLIC_MAX_CONNECTIONS_PER_PEER" envDefault:"256"`
	PublicMaxConnectionsPerTarget     int    `env:"PUBLIC_MAX_CONNECTIONS_PER_TARGET" envDefault:"256"`
	BootstrapAgentID                  string `env:"BOOTSTRAP_AGENT_ID"`
	BootstrapAgentName                string `env:"BOOTSTRAP_AGENT_NAME"`
	BootstrapAgentToken               string `env:"BOOTSTRAP_AGENT_TOKEN"`
	ObservabilityRetentionDays        int    `env:"OBSERVABILITY_RETENTION_DAYS" envDefault:"30"`
	ObservabilityMaxRows              int64  `env:"OBSERVABILITY_MAX_ROWS" envDefault:"1000000"`
	LoginThrottleMaxKeys              int    `env:"LOGIN_THROTTLE_MAX_KEYS" envDefault:"50000"`
	TunnelMaxStreamWindowBytes        int64  `env:"TUNNEL_MAX_STREAM_WINDOW_BYTES" envDefault:"2097152"`
	TunnelMaxConcurrentRequests       int64  `env:"TUNNEL_MAX_CONCURRENT_REQUESTS" envDefault:"64"`
	ServerTunnelMaxConcurrentStreams  int64  `env:"SERVER_TUNNEL_MAX_CONCURRENT_STREAMS" envDefault:"0"`
	ServerTunnelMemoryPercent         int64  `env:"SERVER_TUNNEL_MEMORY_PERCENT" envDefault:"50"`
	ServerTunnelMemoryReserveBytes    int64  `env:"SERVER_TUNNEL_MEMORY_RESERVE_BYTES" envDefault:"536870912"`
	ServerTunnelMemorySoftPercent     int64  `env:"SERVER_TUNNEL_MEMORY_SOFT_PERCENT" envDefault:"80"`
	ServerTunnelMemoryHardPercent     int64  `env:"SERVER_TUNNEL_MEMORY_HARD_PERCENT" envDefault:"90"`
	ServerTunnelMemoryRecoveryPercent int64  `env:"SERVER_TUNNEL_MEMORY_RECOVERY_PERCENT" envDefault:"75"`
	ServerTunnelMemorySampleMillis    int64  `env:"SERVER_TUNNEL_MEMORY_SAMPLE_MILLIS" envDefault:"100"`
	ServerTunnelEstimatedStreamBytes  int64  `env:"SERVER_TUNNEL_ESTIMATED_STREAM_BYTES" envDefault:"1310720"`

	CertsDir                         string `env:"-"`
	ManagementTLSEnabled             bool   `env:"-"`
	ManagementTLSAutoGenerated       bool   `env:"-"`
	ManagementDefaultURL             string `env:"-"`
	ManagementCAPEM                  string `env:"-"`
	ManagementCASHA256               string `env:"-"`
	ManagementDetectedAdvertiseHost  string `env:"-"`
	PublicMaxConcurrentPerTargetAuto bool   `env:"-"`
	ServerTunnelCapacityAuto         bool   `env:"-"`
	ServerTunnelDetectedMemoryBytes  int64  `env:"-"`
	AgentUpdateCatalogStateFile      string `env:"-"`
}

var agentUpdateRepositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Load reads .env files and environment variables into the Config struct.
func Load() (*Config, error) {
	// Attempt to load .env file; it's okay if it doesn't exist.
	_ = godotenv.Load()

	_, explicitDatabaseURL := os.LookupEnv("DATABASE_URL")
	_, explicitPublicClientLimit := os.LookupEnv("PUBLIC_MAX_CONCURRENT_REQUESTS_PER_CLIENT")
	_, explicitPublicPeerLimit := os.LookupEnv("PUBLIC_MAX_CONNECTIONS_PER_PEER")

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}
	// New fairness defaults must not make an existing lower global limit fail
	// startup after an upgrade. Only automatic defaults are narrowed; explicit
	// contradictory values still fail validation below.
	if !explicitPublicClientLimit && cfg.PublicMaxConcurrentPerClient > cfg.PublicMaxConcurrentRequests {
		cfg.PublicMaxConcurrentPerClient = cfg.PublicMaxConcurrentRequests
	}
	if !explicitPublicPeerLimit && cfg.PublicMaxConcurrentConnections > 0 && cfg.PublicMaxConnectionsPerPeer > cfg.PublicMaxConcurrentConnections {
		cfg.PublicMaxConnectionsPerPeer = cfg.PublicMaxConcurrentConnections
	}
	if err := resolveServerTunnelCapacity(cfg, DetectProcessMemoryLimitBytes()); err != nil {
		return nil, err
	}
	if err := validateManagementTLSConfig(cfg); err != nil {
		return nil, err
	}

	if strings.TrimSpace(cfg.ConfigDir) == "" {
		cfg.ConfigDir = DefaultConfigDir
	}
	cfg.ConfigDir = filepath.Clean(cfg.ConfigDir)
	cfg.CertsDir = filepath.Join(cfg.ConfigDir, certsDirName)
	catalogStateName := "agent-update-catalog-state.json"
	if cfg.AgentUpdateChannel == releaseversion.ChannelStaging {
		catalogStateName = "agent-update-catalog-state-staging.json"
	}
	cfg.AgentUpdateCatalogStateFile = filepath.Join(cfg.ConfigDir, catalogStateName)
	authorityKeyPath := strings.TrimSpace(cfg.AgentUpdateAuthorityKeyFile)
	if authorityKeyPath == "" {
		authorityKeyPath = filepath.Join(cfg.ConfigDir, "agent-update-management-authority.json")
	}
	authorityKeyPath, err := filepath.Abs(filepath.Clean(authorityKeyPath))
	if err != nil {
		return nil, fmt.Errorf("resolve AGENT_UPDATE_AUTHORITY_KEY_FILE: %w", err)
	}
	cfg.AgentUpdateAuthorityKeyFile = authorityKeyPath
	if strings.TrimSpace(cfg.PublicCacheDir) == "" {
		cfg.PublicCacheDir = filepath.Join(cfg.ConfigDir, "cache", "public")
	} else {
		cfg.PublicCacheDir = filepath.Clean(cfg.PublicCacheDir)
	}

	if err := prepareConfigDir(cfg.ConfigDir, cfg.CertsDir); err != nil {
		return nil, err
	}

	if !explicitDatabaseURL || strings.TrimSpace(cfg.DatabaseURL) == "" {
		dbPath := filepath.Join(cfg.ConfigDir, databaseFileName)
		if err := migrateLegacyDefaultDatabase(dbPath); err != nil {
			return nil, err
		}
		cfg.DatabaseURL = defaultDatabaseURL(dbPath)
	}

	return cfg, nil
}

func validateManagementTLSConfig(cfg *Config) error {
	cfg.ManagementBindAddress = strings.TrimSpace(cfg.ManagementBindAddress)
	if cfg.ManagementBindAddress == "" {
		cfg.ManagementBindAddress = "0.0.0.0"
	}
	if cfg.ObservabilityMaxRows < 0 {
		return errors.New("OBSERVABILITY_MAX_ROWS must be greater than or equal to 0")
	}
	if cfg.LoginThrottleMaxKeys <= 0 {
		return errors.New("LOGIN_THROTTLE_MAX_KEYS must be greater than 0")
	}
	if cfg.PublicMaxHeaderBytes < 16*1024 || cfg.PublicMaxHeaderBytes > 1024*1024 {
		return errors.New("PUBLIC_MAX_HEADER_BYTES must be between 16384 and 1048576")
	}
	if cfg.PublicMaxRequestBodyBytes < 1 || cfg.PublicMaxRequestBodyBytes > 1<<40 {
		return errors.New("PUBLIC_MAX_REQUEST_BODY_BYTES must be between 1 and 1099511627776")
	}
	if cfg.PublicRequestBodyIdleMillis < 5000 || cfg.PublicRequestBodyIdleMillis > 10*60*1000 {
		return errors.New("PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS must be between 5000 and 600000")
	}
	if cfg.PublicMaxConcurrentRequests < 1 || cfg.PublicMaxConcurrentRequests > 100000 {
		return errors.New("PUBLIC_MAX_CONCURRENT_REQUESTS must be between 1 and 100000")
	}
	if cfg.PublicMaxConcurrentPerTarget < 0 || cfg.PublicMaxConcurrentPerTarget > cfg.PublicMaxConcurrentRequests {
		return errors.New("PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET must be 0 or between 1 and PUBLIC_MAX_CONCURRENT_REQUESTS")
	}
	if cfg.PublicMaxConcurrentPerTarget == 0 {
		cfg.PublicMaxConcurrentPerTarget = cfg.PublicMaxConcurrentRequests
		cfg.PublicMaxConcurrentPerTargetAuto = true
	}
	if cfg.PublicMaxConcurrentPerClient < 0 || cfg.PublicMaxConcurrentPerClient > cfg.PublicMaxConcurrentRequests {
		return errors.New("PUBLIC_MAX_CONCURRENT_REQUESTS_PER_CLIENT must be 0 or between 1 and PUBLIC_MAX_CONCURRENT_REQUESTS")
	}
	if cfg.PublicMaxConcurrentConnections < 0 || cfg.PublicMaxConcurrentConnections > 1_000_000 {
		return errors.New("PUBLIC_MAX_CONCURRENT_CONNECTIONS must be 0 or between 1 and 1000000")
	}
	if cfg.PublicMaxConnectionsPerPeer < 0 || cfg.PublicMaxConnectionsPerPeer > 1_000_000 {
		return errors.New("PUBLIC_MAX_CONNECTIONS_PER_PEER must be 0 or between 1 and 1000000")
	}
	if cfg.PublicMaxConcurrentConnections > 0 && cfg.PublicMaxConnectionsPerPeer > cfg.PublicMaxConcurrentConnections {
		return errors.New("PUBLIC_MAX_CONNECTIONS_PER_PEER must not exceed PUBLIC_MAX_CONCURRENT_CONNECTIONS")
	}
	if cfg.PublicMaxConnectionsPerTarget < 1 || cfg.PublicMaxConnectionsPerTarget > 65535 {
		return errors.New("PUBLIC_MAX_CONNECTIONS_PER_TARGET must be between 1 and 65535")
	}
	if _, err := tunnel.NormalizeMaxStreamWindowSizeBytes(cfg.TunnelMaxStreamWindowBytes); err != nil {
		return err
	}
	if _, err := tunnel.NormalizeMaxConcurrentAgentRequests(cfg.TunnelMaxConcurrentRequests); err != nil {
		return err
	}
	if cfg.ServerTunnelMaxConcurrentStreams < 1 || cfg.ServerTunnelMaxConcurrentStreams > tunnel.MaxServerConcurrentStreamsLimit {
		return fmt.Errorf("SERVER_TUNNEL_MAX_CONCURRENT_STREAMS must be between 1 and %d", tunnel.MaxServerConcurrentStreamsLimit)
	}
	if cfg.ServerTunnelMemoryRecoveryPercent < 1 || cfg.ServerTunnelMemoryRecoveryPercent >= cfg.ServerTunnelMemorySoftPercent {
		return errors.New("SERVER_TUNNEL_MEMORY_RECOVERY_PERCENT must be positive and below SERVER_TUNNEL_MEMORY_SOFT_PERCENT")
	}
	if cfg.ServerTunnelMemorySoftPercent < 1 || cfg.ServerTunnelMemorySoftPercent >= cfg.ServerTunnelMemoryHardPercent {
		return errors.New("SERVER_TUNNEL_MEMORY_SOFT_PERCENT must be positive and below SERVER_TUNNEL_MEMORY_HARD_PERCENT")
	}
	if cfg.ServerTunnelMemoryHardPercent > 99 {
		return errors.New("SERVER_TUNNEL_MEMORY_HARD_PERCENT must be at most 99")
	}
	if cfg.ServerTunnelMemorySampleMillis < 10 || cfg.ServerTunnelMemorySampleMillis > 10000 {
		return errors.New("SERVER_TUNNEL_MEMORY_SAMPLE_MILLIS must be between 10 and 10000")
	}
	if cfg.ServerTunnelEstimatedStreamBytes < tunnel.MinimumAdaptiveStreamChargeBytes || cfg.ServerTunnelEstimatedStreamBytes > tunnel.MaxStreamWindowSizeBytesLimit {
		return fmt.Errorf("SERVER_TUNNEL_ESTIMATED_STREAM_BYTES must be between %d and %d", tunnel.MinimumAdaptiveStreamChargeBytes, tunnel.MaxStreamWindowSizeBytesLimit)
	}
	if _, err := tunnel.AdaptiveMaxStreamWindowSizeBytes(cfg.TunnelMaxStreamWindowBytes, cfg.ServerTunnelEstimatedStreamBytes); err != nil {
		return err
	}
	cfg.ManagementTLSCertFile = strings.TrimSpace(cfg.ManagementTLSCertFile)
	cfg.ManagementTLSKeyFile = strings.TrimSpace(cfg.ManagementTLSKeyFile)
	cfg.ManagementTLSClientCAFile = strings.TrimSpace(cfg.ManagementTLSClientCAFile)
	cfg.ManagementTLSMode = strings.ToLower(strings.TrimSpace(cfg.ManagementTLSMode))
	cfg.ManagementPublicURL = strings.TrimSpace(cfg.ManagementPublicURL)
	cfg.ManagementSetupToken = strings.TrimSpace(cfg.ManagementSetupToken)
	cfg.ManagementTrustedProxyCIDRs = strings.TrimSpace(cfg.ManagementTrustedProxyCIDRs)
	cfg.ManagementClientIPHeader = strings.TrimSpace(cfg.ManagementClientIPHeader)
	cfg.ManagementClientIPMode = strings.ToLower(strings.TrimSpace(cfg.ManagementClientIPMode))
	cfg.ManagementAdvertiseHost = strings.TrimSpace(cfg.ManagementAdvertiseHost)
	cfg.ManagementTLSExtraHosts = strings.TrimSpace(cfg.ManagementTLSExtraHosts)
	cfg.AgentUpdateRepository = strings.TrimSpace(cfg.AgentUpdateRepository)
	agentUpdateChannel, err := resolveAgentUpdateChannel(cfg.AgentUpdateChannel, buildinfo.Channel)
	if err != nil {
		return err
	}
	cfg.AgentUpdateChannel = agentUpdateChannel
	cfg.AgentUpdateAuthorityKeyFile = strings.TrimSpace(cfg.AgentUpdateAuthorityKeyFile)
	if !agentUpdateRepositoryRE.MatchString(cfg.AgentUpdateRepository) {
		return errors.New("AGENT_UPDATE_REPOSITORY must use GitHub owner/repo syntax")
	}
	if cfg.AgentUpdateChannel != releaseversion.ChannelStable && cfg.AgentUpdateChannel != releaseversion.ChannelStaging {
		return errors.New("AGENT_UPDATE_CHANNEL must be stable or staging")
	}
	if cfg.AgentUpdateCatalogRefreshMillis < 10_000 || cfg.AgentUpdateCatalogRefreshMillis > 3_600_000 {
		return errors.New("AGENT_UPDATE_CATALOG_REFRESH_MILLIS must be between 10000 and 3600000")
	}
	if cfg.AgentUpdateHTTPTimeoutMillis < 1_000 || cfg.AgentUpdateHTTPTimeoutMillis > 60_000 {
		return errors.New("AGENT_UPDATE_HTTP_TIMEOUT_MILLIS must be between 1000 and 60000")
	}
	cfg.BootstrapAgentID = strings.TrimSpace(cfg.BootstrapAgentID)
	cfg.BootstrapAgentName = strings.TrimSpace(cfg.BootstrapAgentName)
	cfg.BootstrapAgentToken = strings.TrimSpace(cfg.BootstrapAgentToken)
	if err := validateConfiguredSecret("MANAGEMENT_SETUP_TOKEN", cfg.ManagementSetupToken); err != nil {
		return err
	}
	if err := validateConfiguredSecret("BOOTSTRAP_AGENT_TOKEN", cfg.BootstrapAgentToken); err != nil {
		return err
	}
	if err := validateManagementTrustedProxyConfig(cfg); err != nil {
		return err
	}
	if cfg.ManagementTLSMode == "" {
		cfg.ManagementTLSMode = "auto"
	}
	switch cfg.ManagementTLSMode {
	case "auto", "provided", "off":
	default:
		return errors.New("MANAGEMENT_TLS_MODE must be auto, provided, or off")
	}

	hasCert := cfg.ManagementTLSCertFile != ""
	hasKey := cfg.ManagementTLSKeyFile != ""
	if hasCert != hasKey {
		return errors.New("MANAGEMENT_TLS_CERT_FILE and MANAGEMENT_TLS_KEY_FILE must be set together")
	}
	if cfg.ManagementTLSMode == "provided" && (!hasCert || !hasKey) {
		return errors.New("MANAGEMENT_TLS_MODE=provided requires MANAGEMENT_TLS_CERT_FILE and MANAGEMENT_TLS_KEY_FILE")
	}
	if cfg.ManagementTLSMode == "off" {
		if cfg.ManagementTLSClientCAFile != "" {
			return errors.New("MANAGEMENT_TLS_CLIENT_CA_FILE requires management TLS")
		}
		if !cfg.ManagementAllowInsecureHTTP {
			return errors.New("MANAGEMENT_TLS_MODE=off requires MANAGEMENT_ALLOW_INSECURE_HTTP=true")
		}
	}
	if cfg.ManagementTLSClientCAFile != "" && cfg.ManagementTLSMode == "off" {
		return errors.New("MANAGEMENT_TLS_CLIENT_CA_FILE requires management TLS")
	}
	if cfg.ManagementPublicURL != "" {
		parsed, err := url.Parse(cfg.ManagementPublicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid MANAGEMENT_PUBLIC_URL %q", cfg.ManagementPublicURL)
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && cfg.ManagementTLSMode == "off" && cfg.ManagementAllowInsecureHTTP) {
			return errors.New("MANAGEMENT_PUBLIC_URL must use https unless MANAGEMENT_TLS_MODE=off and MANAGEMENT_ALLOW_INSECURE_HTTP=true")
		}
	}
	return nil
}

func resolveAgentUpdateChannel(configured, compiled string) (string, error) {
	configured = strings.ToLower(strings.TrimSpace(configured))
	compiled = strings.ToLower(strings.TrimSpace(compiled))
	compiledIsRelease := compiled == releaseversion.ChannelStable || compiled == releaseversion.ChannelStaging
	if configured == "" {
		if compiledIsRelease {
			return compiled, nil
		}
		return releaseversion.ChannelStable, nil
	}
	if configured != releaseversion.ChannelStable && configured != releaseversion.ChannelStaging {
		return "", errors.New("AGENT_UPDATE_CHANNEL must be stable or staging")
	}
	if compiledIsRelease && configured != compiled {
		return "", errors.New("AGENT_UPDATE_CHANNEL must match the compiled release channel")
	}
	return configured, nil
}

func validateConfiguredSecret(name, value string) error {
	if value == "" {
		return nil
	}
	if len(value) < 32 {
		return fmt.Errorf("%s must contain at least 32 characters; generate configured secrets with cryptographically random data", name)
	}
	if len(value) > 4096 {
		return fmt.Errorf("%s must not exceed 4096 characters", name)
	}
	return nil
}

func validateManagementTrustedProxyConfig(cfg *Config) error {
	if cfg.ManagementClientIPHeader == "" || !validHTTPHeaderName(cfg.ManagementClientIPHeader) {
		return errors.New("MANAGEMENT_CLIENT_IP_HEADER must be a valid HTTP header name")
	}
	switch cfg.ManagementClientIPMode {
	case "single_ip", "trusted_chain":
	default:
		return errors.New("MANAGEMENT_CLIENT_IP_MODE must be single_ip or trusted_chain")
	}
	for _, raw := range splitConfigList(cfg.ManagementTrustedProxyCIDRs) {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" {
			return fmt.Errorf("MANAGEMENT_TRUSTED_PROXY_CIDRS contains invalid CIDR %q", raw)
		}
		bits := prefix.Bits()
		if prefix.Addr().Is4In6() {
			bits -= 96
		}
		if bits < 0 || bits > prefix.Addr().Unmap().BitLen() {
			return fmt.Errorf("MANAGEMENT_TRUSTED_PROXY_CIDRS contains invalid CIDR %q", raw)
		}
		if bits == 0 {
			return fmt.Errorf("MANAGEMENT_TRUSTED_PROXY_CIDRS must not trust an entire address family (%q)", raw)
		}
	}
	return nil
}

func splitConfigList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func prepareConfigDir(configDir, certsDir string) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create CONFIG_DIR %q: %w", configDir, err)
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return fmt.Errorf("failed to set permissions on CONFIG_DIR %q: %w", configDir, err)
	}
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		return fmt.Errorf("failed to create certs directory %q: %w", certsDir, err)
	}
	if err := os.Chmod(certsDir, 0700); err != nil {
		return fmt.Errorf("failed to set permissions on certs directory %q: %w", certsDir, err)
	}
	return nil
}

func (c *Config) PublicTLSCertificatePaths(listenerID, mappingID int64) (certPath, keyPath string) {
	certsDir := c.CertsDir
	if strings.TrimSpace(certsDir) == "" {
		configDir := c.ConfigDir
		if strings.TrimSpace(configDir) == "" {
			configDir = DefaultConfigDir
		}
		certsDir = filepath.Join(filepath.Clean(configDir), certsDirName)
	}

	dir := filepath.Join(certsDir, fmt.Sprintf("public-listener-%d", listenerID))
	return filepath.Join(dir, fmt.Sprintf("tls-%d.crt.pem", mappingID)),
		filepath.Join(dir, fmt.Sprintf("tls-%d.key.pem", mappingID))
}

func (c *Config) WritePublicTLSCertificateFiles(listenerID, mappingID int64, certPEM, keyPEM []byte) (certPath, keyPath string, err error) {
	certPath, keyPath = c.PublicTLSCertificatePaths(listenerID, mappingID)
	dir := filepath.Dir(certPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create TLS certificate directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS certificate directory %q: %w", dir, err)
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return "", "", fmt.Errorf("failed to write TLS certificate %q: %w", certPath, err)
	}
	if err := os.Chmod(certPath, 0600); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS certificate %q: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", "", fmt.Errorf("failed to write TLS private key %q: %w", keyPath, err)
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS private key %q: %w", keyPath, err)
	}
	return certPath, keyPath, nil
}

func defaultDatabaseURL(dbPath string) string {
	values := url.Values{}
	values.Set("mode", "rwc")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "10000")
	values.Set("_txlock", "immediate")
	values.Set("_fk", "1")
	values.Set("cache", "private")
	return "file:" + filepath.ToSlash(dbPath) + "?" + values.Encode()
}

func migrateLegacyDefaultDatabase(newDBPath string) error {
	if _, err := os.Stat(newDBPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect configured database %q: %w", newDBPath, err)
	}

	legacyDBPath := databaseFileName
	if samePath(legacyDBPath, newDBPath) {
		return nil
	}
	if _, err := os.Stat(legacyDBPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to inspect legacy database %q: %w", legacyDBPath, err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := legacyDBPath + suffix
		dst := newDBPath + suffix
		if err := copyFileIfExists(src, dst); err != nil {
			return fmt.Errorf("failed to migrate legacy database file %q to %q: %w", src, dst, err)
		}
	}
	return nil
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}

func copyFileIfExists(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("source is a directory")
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0600
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}
