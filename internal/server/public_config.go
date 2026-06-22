package server

import (
	"database/sql"
	"net/http"
	"regexp"

	"p2pstream/internal/db"
)

const (
	defaultPublicHTTPPort                                     = int64(80)
	defaultPublicRoutePriority                                = int64(1000)
	defaultPublicSelfSignedValidityDays                       = int64(3650)
	minPublicSelfSignedValidityDays                           = int64(1)
	maxPublicSelfSignedValidityDays                           = int64(3650)
	defaultSelfSignedTLSHost                                  = "p2pstream.local"
	defaultWelcomeContentType                                 = "text/html; charset=utf-8"
	defaultWelcomeCacheControl                                = "no-store"
	publicResponseBodyModeInline                              = "inline"
	publicResponseBodyModeTemplate                            = "template"
	publicResponseTemplateKindGenericBody                     = "generic_body"
	publicResponseTemplateKindWafCaptchaPage                  = "waf_captcha_page"
	publicResponseTemplateKindWafWaitingRoomPage              = "waf_waiting_room_page"
	defaultResponseTemplateContentType                        = "text/html; charset=utf-8"
	publicRouteTargetTypeProxy                                = "proxy"
	publicRouteTargetTypeStatic                               = "static"
	publicRouteTargetTransportDirect                          = "direct"
	publicRouteTargetTransportAgent                           = "agent"
	publicRouteTargetLoadBalancingRoundRobin                  = "round_robin"
	publicRouteTargetLoadBalancingWeightedRoundRobin          = "weighted_round_robin"
	publicRouteTargetLoadBalancingRandom                      = "random"
	publicRouteTargetLoadBalancingWeightedRandom              = "weighted_random"
	publicRouteTargetLoadBalancingLeastActiveRequests         = "least_active_requests"
	publicRouteTargetLoadBalancingWeightedLeastActiveRequests = "weighted_least_active_requests"
	publicRouteActionForward                                  = "forward"
	publicRouteActionRedirect                                 = "redirect"
	publicRouteRedirectTargetModeSameHostPath                 = "same_host_path"
	publicRouteRedirectTargetModeExternalOriginKeepPath       = "external_origin_keep_path"
	publicRouteRedirectTargetModeAbsoluteURL                  = "absolute_url"
	publicRoutePathSecurityModeStrict                         = "strict"
	publicRoutePathSecurityModeAllowEncodedSeparators         = "allow_encoded_separators"
	publicRateLimitAlgorithmFixedWindow                       = "fixed_window"
	publicRateLimitAlgorithmSlidingWindow                     = "sliding_window"
	publicRateLimitAlgorithmTokenBucket                       = "token_bucket"
	publicRateLimitAlgorithmLeakyBucket                       = "leaky_bucket"
	publicTLSCertificateSourceManual                          = "manual"
	publicTLSCertificateSourceACME                            = "acme"
	publicACMEChallengeHTTP01                                 = "http_01"
	publicACMEChallengeTLSALPN01                              = "tls_alpn_01"
	publicACMEChallengeDNS01                                  = "dns_01"
	publicACMECAProduction                                    = "letsencrypt_production"
	publicACMECAStaging                                       = "letsencrypt_staging"
	publicDNSProviderCloudflare                               = "cloudflare"
	publicTLSCertificateStatusPending                         = "pending"
	publicTLSCertificateStatusReady                           = "ready"
	publicTLSCertificateStatusRenewing                        = "renewing"
	publicTLSCertificateStatusError                           = "error"
	defaultStaticStatusCode                                   = int64(http.StatusOK)
	defaultRedirectStatusCode                                 = int64(http.StatusFound)
	defaultTargetHealthCheckMethod                            = http.MethodGet
	defaultTargetHealthCheckPath                              = "/"
	defaultTargetHealthCheckIntervalMillis                    = int64(10000)
	defaultTargetHealthCheckTimeoutMillis                     = int64(2000)
	defaultTargetHealthCheckHealthyThreshold                  = int64(2)
	defaultTargetHealthCheckUnhealthyThreshold                = int64(2)
	defaultTargetHealthCheckExpectedStatusMin                 = int64(200)
	defaultTargetHealthCheckExpectedStatusMax                 = int64(399)
	defaultTargetUpstreamResponseHeaderTimeoutMillis          = int64(60000)
	minTargetUpstreamResponseHeaderTimeoutMillis              = int64(1000)
	maxTargetUpstreamResponseHeaderTimeoutMillis              = int64(3600000)
	maxUpstreamHeaderValueBytes                               = 8192
)

var publicNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type publicConfigRows struct {
	Agents                     []db.Agent
	AgentLabels                []db.PublicAgentLabel
	Listeners                  []db.PublicListener
	Routes                     []db.PublicRoute
	RouteTargets               []db.PublicRouteTarget
	RouteTargetUpstreamHeaders []db.PublicRouteTargetUpstreamHeader
	RouteTargetResponseHeaders []db.PublicRouteTargetResponseHeader
	TLSCertificates            []db.PublicTlsCertificate
	TLSDNSCredentials          []db.PublicTlsDnsCredential
	RateLimitRules             []db.PublicRateLimitRule
	TrafficShaperRules         []db.PublicTrafficShaperRule
	WafCaptchaProviders        []db.PublicWafCaptchaProvider
	WafRules                   []db.PublicWafRule
	WafSettings                db.PublicWafSetting
	CacheSettings              db.PublicCacheSetting
	CacheRules                 []db.PublicCacheRule
	ResponseTemplates          []db.PublicResponseTemplate
}

type cachedPublicConfig struct {
	Rows     publicConfigRows
	Snapshot *publicProxySnapshot
	Valid    bool
}

type publicRouteTargetResponseHeaderInput struct {
	Name  string
	Value string
}

type publicRouteTargetUpstreamHeaderInput struct {
	ID        int64
	Name      string
	Value     string
	Sensitive int64
}

type existingSensitiveUpstreamHeaderValue struct {
	TargetID int64
	Name     string
	Value    string
}

type existingPublicRouteTargetSecrets struct {
	UpstreamHeaders    map[int64]existingSensitiveUpstreamHeaderValue
	BasicAuthPasswords map[int64]string
}

type publicRouteTargetMutationInput struct {
	Params          db.CreatePublicRouteTargetParams
	UpstreamHeaders []publicRouteTargetUpstreamHeaderInput
	ResponseHeaders []publicRouteTargetResponseHeaderInput
}

type publicTLSCertificateMutationInput struct {
	ID                   int64
	ListenerID           int64
	HostnamePattern      string
	CertPath             string
	KeyPath              string
	Enabled              int64
	Source               string
	ACMEChallengeType    string
	ACMECA               string
	ACMEEmail            string
	DNSCredentialID      sql.NullInt64
	Status               string
	LastError            string
	IssuedAt             sql.NullTime
	ExpiresAt            sql.NullTime
	NextRenewalAt        sql.NullTime
	LastRenewalAttemptAt sql.NullTime
}

type publicTLSCertificateMaterial struct {
	Replace bool
	CertPEM []byte
	KeyPEM  []byte
}
