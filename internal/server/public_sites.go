package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/idna"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const maxPublicSiteHosts = 64

const (
	publicSiteHostRolePrimary               = "primary"
	publicSiteHostRoleAlias                 = "alias"
	publicSiteHostBehaviorServe             = "serve"
	publicSiteHostBehaviorRedirect          = "redirect"
	publicSiteListenerBehaviorServe         = "serve"
	publicSiteListenerBehaviorRedirectHTTPS = "redirect_https"
)

type publicSiteHostMutation struct {
	HostnamePattern string
	Role            string
	Behavior        string
}

type publicSiteListenerBindingMutation struct {
	ListenerID         int64
	Behavior           string
	RedirectListenerID sql.NullInt64
	RedirectHostname   string
}

func normalizePublicSiteHostnamePattern(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return "", errors.New("hostname is required")
	}
	wildcard := strings.HasPrefix(value, "*.")
	if wildcard {
		value = strings.TrimPrefix(value, "*.")
	}
	if strings.Contains(value, "*") || strings.ContainsAny(value, "/\\@[] ") {
		return "", errors.New("hostname must be an exact host or a wildcard like *.example.com")
	}
	if address, parseErr := netip.ParseAddr(value); parseErr == nil {
		if address.Zone() != "" {
			return "", errors.New("IP zone identifiers are not allowed")
		}
		if wildcard {
			return "", errors.New("IP addresses cannot be wildcarded")
		}
		return address.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("hostname is not valid IDNA: %w", err)
	}
	if len(ascii) > 253 || !validPublicSiteDNSName(ascii) {
		return "", errors.New("hostname is not a valid DNS name")
	}
	if wildcard {
		if !strings.Contains(ascii, ".") {
			return "", errors.New("wildcard hostname must include a registrable suffix")
		}
		return "*." + ascii, nil
	}
	return ascii, nil
}

// canonicalPublicHostnamePatternOrLegacy keeps read paths tolerant of rows
// written before public hostname inputs were normalized as IDNA. Mutation paths
// must use normalizePublicSiteHostnamePattern and surface its validation error.
func canonicalPublicHostnamePatternOrLegacy(value string) string {
	canonical, err := normalizePublicSiteHostnamePattern(value)
	if err != nil {
		return normalizeHostPattern(value)
	}
	return canonical
}

func normalizePublicSiteRequestDNSName(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, "*") {
		return "", errors.New("invalid hostname")
	}
	value = strings.ToLower(value)
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
		if strings.HasSuffix(value, ".") {
			return "", errors.New("hostname has multiple trailing dots")
		}
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("hostname is not valid IDNA: %w", err)
	}
	if len(ascii) == 0 || len(ascii) > 253 {
		return "", errors.New("hostname is not a valid DNS name")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("hostname is not a valid DNS name")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", errors.New("hostname is not a valid DNS name")
			}
		}
	}
	return ascii, nil
}

func validPublicSiteDNSName(value string) bool {
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validatePublicSiteHosts(inputs []*p2pstreamv1.PublicSiteHostInput) ([]publicSiteHostMutation, error) {
	if len(inputs) > maxPublicSiteHosts {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site supports at most %d hostnames", maxPublicSiteHosts))
	}
	seen := make(map[string]struct{}, len(inputs))
	result := make([]publicSiteHostMutation, 0, len(inputs))
	primaryCount := 0
	for _, input := range inputs {
		if input == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site hostname entry is required"))
		}
		hostname, err := normalizePublicSiteHostnamePattern(input.HostnamePattern)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if _, exists := seen[hostname]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate site hostname %q", hostname))
		}
		seen[hostname] = struct{}{}
		behavior := publicSiteHostBehaviorServe
		switch input.Behavior {
		case p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_UNSPECIFIED,
			p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE:
		case p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT:
			behavior = publicSiteHostBehaviorRedirect
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site hostname behavior must be serve or redirect"))
		}
		role := publicSiteHostRoleAlias
		if input.Primary {
			primaryCount++
			role = publicSiteHostRolePrimary
			behavior = publicSiteHostBehaviorServe
			if strings.HasPrefix(hostname, "*.") {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("primary hostname must be exact"))
			}
		}
		result = append(result, publicSiteHostMutation{HostnamePattern: hostname, Role: role, Behavior: behavior})
	}
	if primaryCount > 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site supports at most one compatibility primary hostname"))
	}
	return result, nil
}

func validatePublicSiteCanonicalHostname(value string, hosts []publicSiteHostMutation) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		for _, host := range hosts {
			if host.Role == publicSiteHostRolePrimary {
				return host.HostnamePattern, nil
			}
		}
		return "", nil
	}
	canonical, err := normalizePublicSiteHostnamePattern(value)
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("canonical hostname: %w", err))
	}
	if strings.HasPrefix(canonical, "*.") {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("canonical hostname must be exact"))
	}
	for _, host := range hosts {
		if host.HostnamePattern == canonical && host.Behavior == publicSiteHostBehaviorServe {
			return canonical, nil
		}
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("canonical hostname must be one of the Site's Serve hostnames"))
}

func mirrorPublicSiteCanonicalRole(hosts []publicSiteHostMutation, canonical string) {
	for i := range hosts {
		hosts[i].Role = publicSiteHostRoleAlias
		if canonical != "" && hosts[i].HostnamePattern == canonical {
			hosts[i].Role = publicSiteHostRolePrimary
		}
	}
}

func validatePublicSiteListenerBindings(ctx context.Context, q db.Querier, legacyListenerID int64, inputs []*p2pstreamv1.PublicSiteListenerBinding) ([]publicSiteListenerBindingMutation, error) {
	if len(inputs) == 0 && legacyListenerID > 0 {
		inputs = []*p2pstreamv1.PublicSiteListenerBinding{{ListenerId: legacyListenerID, Behavior: p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE}}
	}
	seen := make(map[int64]struct{}, len(inputs))
	result := make([]publicSiteListenerBindingMutation, 0, len(inputs))
	for _, input := range inputs {
		if input == nil || input.ListenerId <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Site listener binding requires a listener"))
		}
		if _, exists := seen[input.ListenerId]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("listener %d is assigned more than once", input.ListenerId))
		}
		seen[input.ListenerId] = struct{}{}
		if _, err := q.GetPublicListener(ctx, input.ListenerId); err != nil {
			return nil, publicDBError(err)
		}
		binding := publicSiteListenerBindingMutation{ListenerID: input.ListenerId, Behavior: publicSiteListenerBehaviorServe}
		switch input.Behavior {
		case p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_UNSPECIFIED,
			p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE:
		case p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS:
			binding.Behavior = publicSiteListenerBehaviorRedirectHTTPS
			if input.RedirectListenerId > 0 {
				if _, err := q.GetPublicListener(ctx, input.RedirectListenerId); err != nil {
					return nil, publicDBError(err)
				}
				binding.RedirectListenerID = sql.NullInt64{Int64: input.RedirectListenerId, Valid: true}
			}
			if input.RedirectHostname != "" {
				hostname, err := normalizePublicSiteHostnamePattern(input.RedirectHostname)
				if err != nil || strings.HasPrefix(hostname, "*.") {
					return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("redirect hostname must be an exact hostname"))
				}
				binding.RedirectHostname = hostname
			}
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Site listener behavior must be Serve or Redirect to HTTPS"))
		}
		result = append(result, binding)
	}
	return result, nil
}

func (s *publicConfigService) publicSiteProto(ctx context.Context, siteID int64) (*p2pstreamv1.PublicSite, error) {
	site, err := s.db.GetPublicSite(ctx, siteID)
	if err != nil {
		return nil, publicDBError(err)
	}
	hosts, err := s.db.ListPublicSiteHostsBySite(ctx, siteID)
	if err != nil {
		return nil, publicDBError(err)
	}
	bindings, err := s.db.ListPublicSiteListenerBindingsBySite(ctx, siteID)
	if err != nil {
		return nil, publicDBError(err)
	}
	allSites, err := s.db.ListPublicSites(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	allHosts, err := s.db.ListPublicSiteHosts(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	allBindings, err := s.db.ListPublicSiteListenerBindings(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	listeners, err := s.db.ListPublicListeners(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	certs, err := s.db.ListPublicTlsCertificates(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	routes, err := s.db.ListPublicRoutes(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	targets, err := s.db.ListPublicRouteTargets(ctx)
	if err != nil {
		return nil, publicDBError(err)
	}
	listenersByID := make(map[int64]db.PublicListener, len(listeners))
	for _, listener := range listeners {
		listenersByID[listener.ID] = listener
	}
	readiness := buildPublicSiteReadiness(site, hosts, bindings, allSites, allHosts, allBindings, listeners, certs, routes, targets)
	return publicSiteToProto(site, hosts, bindings, listenersByID, certs, readiness), nil
}

func validatePublishedSiteTx(ctx context.Context, q *db.Queries, siteID int64) ([]*p2pstreamv1.PublicSiteReadinessItem, error) {
	site, err := q.GetPublicSite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	hosts, err := q.ListPublicSiteHostsBySite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	bindings, err := q.ListPublicSiteListenerBindingsBySite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	allSites, err := q.ListPublicSites(ctx)
	if err != nil {
		return nil, err
	}
	allHosts, err := q.ListPublicSiteHosts(ctx)
	if err != nil {
		return nil, err
	}
	allBindings, err := q.ListPublicSiteListenerBindings(ctx)
	if err != nil {
		return nil, err
	}
	listeners, err := q.ListPublicListeners(ctx)
	if err != nil {
		return nil, err
	}
	certs, err := q.ListPublicTlsCertificates(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := q.ListPublicRoutes(ctx)
	if err != nil {
		return nil, err
	}
	targets, err := q.ListPublicRouteTargets(ctx)
	if err != nil {
		return nil, err
	}
	return buildPublicSiteReadiness(site, hosts, bindings, allSites, allHosts, allBindings, listeners, certs, routes, targets), nil
}

func validatePublishedSitesTx(ctx context.Context, q *db.Queries, candidateSiteID int64, requireCandidateBinding bool) error {
	sites, err := q.ListPublicSites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if site.Published == 0 {
			continue
		}
		readiness, err := validatePublishedSiteTx(ctx, q, site.ID)
		if err != nil {
			return err
		}
		for _, item := range readiness {
			if item.Severity != p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_BLOCKER {
				continue
			}
			if site.ID != candidateSiteID && !strings.HasPrefix(item.Code, "site.redirect.") {
				continue
			}
			if site.ID == candidateSiteID && !requireCandidateBinding && site.Enabled == 0 && !strings.HasPrefix(item.Code, "site.redirect.") && item.Code != "site.default.conflict" && item.Code != "site.hostname.conflict" {
				continue
			}
			// A previously published Site may intentionally be retained without a
			// listener. Publishing a new draft still requires an active binding.
			if item.Code == "site.listeners.required" && !requireCandidateBinding {
				continue
			}
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(item.Message))
		}
	}
	return nil
}

func validatePublishedSitesForListenerProtocolChangeTx(ctx context.Context, q *db.Queries, listenerID int64) error {
	sites, err := q.ListPublicSites(ctx)
	if err != nil {
		return err
	}
	bindings, err := q.ListPublicSiteListenerBindings(ctx)
	if err != nil {
		return err
	}
	affected := make(map[int64]struct{})
	for _, binding := range bindings {
		if binding.ListenerID == listenerID || (binding.RedirectListenerID.Valid && binding.RedirectListenerID.Int64 == listenerID) {
			affected[binding.SiteID] = struct{}{}
		}
	}
	for _, site := range sites {
		if site.Published == 0 {
			continue
		}
		if _, ok := affected[site.ID]; !ok {
			continue
		}
		readiness, err := validatePublishedSiteTx(ctx, q, site.ID)
		if err != nil {
			return err
		}
		if err := publicSiteReadinessBlocker(readiness); err != nil {
			return err
		}
	}
	return nil
}

func publicSiteReadinessBlocker(items []*p2pstreamv1.PublicSiteReadinessItem) error {
	for _, item := range items {
		if item.Severity == p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_BLOCKER {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(item.Message))
		}
	}
	return nil
}

func buildPublicSiteReadiness(site db.PublicSite, hosts []db.PublicSiteHost, bindings []db.PublicSiteListenerBinding, allSites []db.PublicSite, allHosts []db.PublicSiteHost, allBindings []db.PublicSiteListenerBinding, listeners []db.PublicListener, certs []db.PublicTlsCertificate, routes []db.PublicRoute, targets []db.PublicRouteTarget) []*p2pstreamv1.PublicSiteReadinessItem {
	var items []*p2pstreamv1.PublicSiteReadinessItem
	blocker := func(code, message, action string, listenerID, routeID int64, hostname string) {
		items = append(items, &p2pstreamv1.PublicSiteReadinessItem{Code: code, Severity: p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_BLOCKER, Message: message, ListenerId: listenerID, RouteId: routeID, Hostname: hostname, Action: action})
	}
	warning := func(code, message, action string, listenerID int64) {
		items = append(items, &p2pstreamv1.PublicSiteReadinessItem{Code: code, Severity: p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_WARNING, Message: message, ListenerId: listenerID, Action: action})
	}
	info := func(code, message string, listenerID int64) {
		items = append(items, &p2pstreamv1.PublicSiteReadinessItem{Code: code, Severity: p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_INFO, Message: message, ListenerId: listenerID})
	}
	unknown := func(code, message, action string, listenerID, routeID int64) {
		items = append(items, &p2pstreamv1.PublicSiteReadinessItem{Code: code, Severity: p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_UNKNOWN, Message: message, ListenerId: listenerID, RouteId: routeID, Action: action})
	}
	if len(bindings) == 0 {
		if site.Published != 0 {
			warning("site.listeners.inactive", "This published Site is not assigned to a listener", "add_listener", 0)
		} else {
			blocker("site.listeners.required", "Assign at least one listener before publishing", "add_listener", 0, 0, "")
		}
	}
	if site.DefaultSite != 0 {
		if len(hosts) != 0 {
			blocker("site.default.hostnames", "A Default Site cannot define hostname claims", "edit_hostnames", 0, 0, "")
		}
	} else if len(hosts) == 0 {
		blocker("site.hostnames.required", "Add at least one hostname before publishing", "edit_hostnames", 0, 0, "")
	}
	hostsBySite := make(map[int64][]db.PublicSiteHost)
	for _, host := range allHosts {
		hostsBySite[host.SiteID] = append(hostsBySite[host.SiteID], host)
	}
	bindingsBySite := make(map[int64][]db.PublicSiteListenerBinding)
	for _, binding := range allBindings {
		bindingsBySite[binding.SiteID] = append(bindingsBySite[binding.SiteID], binding)
	}
	sitesByID := make(map[int64]db.PublicSite)
	for _, other := range allSites {
		sitesByID[other.ID] = other
	}
	listenersByID := make(map[int64]db.PublicListener)
	for _, listener := range listeners {
		listenersByID[listener.ID] = listener
	}
	for _, binding := range bindings {
		listener, exists := listenersByID[binding.ListenerID]
		if !exists {
			blocker("site.listener.missing", fmt.Sprintf("Listener %d no longer exists", binding.ListenerID), "add_listener", binding.ListenerID, 0, "")
			continue
		}
		if listener.Enabled == 0 {
			warning("site.listener.disabled", fmt.Sprintf("Listener %q is disabled", listener.Name), "edit_listener", listener.ID)
		}
		if listener.Protocol == publicListenerProtocolHTTP {
			info("site.tls.not_applicable", "TLS certificate coverage does not apply to this HTTP listener", listener.ID)
		} else if site.DefaultSite != 0 {
			if publicListenerHasUsableTLSCertificate(listener.ID, certs) {
				unknown("site.tls.default.coverage_not_checked", "Default Site TLS coverage depends on each requested hostname and cannot be fully checked", "configure_certificate", listener.ID, 0)
			} else {
				warning("site.tls.default.unconfigured", "This HTTPS Default Site has no usable certificate; only requested hostnames covered by a configured certificate can work", "configure_certificate", listener.ID)
			}
		} else {
			for _, host := range hosts {
				coverage, detail := publicSiteTLSCoverage(listener.Protocol, listener.ID, host.HostnamePattern, certs)
				if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED {
					blocker("site.tls.coverage", fmt.Sprintf("%s: %s", host.HostnamePattern, detail), "configure_certificate", listener.ID, 0, host.HostnamePattern)
				}
			}
		}
		unknown("site.network.not_checked", "DNS and external network reachability are not checked", "", listener.ID, 0)
		for _, otherBinding := range allBindings {
			if otherBinding.ListenerID != binding.ListenerID || otherBinding.SiteID == site.ID {
				continue
			}
			other := sitesByID[otherBinding.SiteID]
			if other.Published == 0 {
				continue
			}
			if site.DefaultSite != 0 && other.DefaultSite != 0 {
				blocker("site.default.conflict", fmt.Sprintf("Listener %q already has a published Default Site", listener.Name), "choose_listener", listener.ID, 0, "")
			}
			if site.DefaultSite == 0 && other.DefaultSite == 0 {
				for _, host := range hosts {
					for _, otherHost := range hostsBySite[other.ID] {
						if host.HostnamePattern == otherHost.HostnamePattern {
							blocker("site.hostname.conflict", fmt.Sprintf("Hostname %q is already owned on listener %q", host.HostnamePattern, listener.Name), "edit_hostnames", listener.ID, 0, host.HostnamePattern)
						}
					}
				}
			}
		}
		if binding.Behavior == publicSiteListenerBehaviorRedirectHTTPS {
			validatePublicSiteRedirectBindingReadiness(&items, site, hosts, binding, bindings, allSites, hostsBySite, bindingsBySite, listenersByID)
			continue
		}
	}
	redirectAlias := false
	canonicalServed := false
	for _, host := range hosts {
		redirectAlias = redirectAlias || host.Behavior == publicSiteHostBehaviorRedirect
		canonicalServed = canonicalServed || (host.HostnamePattern == site.CanonicalHostname && host.Behavior == publicSiteHostBehaviorServe && !strings.HasPrefix(host.HostnamePattern, "*."))
	}
	if redirectAlias && (site.CanonicalHostname == "" || !canonicalServed) {
		blocker("site.canonical.required", "Redirect aliases require an exact canonical Serve hostname", "edit_hostnames", 0, 0, site.CanonicalHostname)
	}
	enabledRoute := false
	targetsByRoute := make(map[int64][]db.PublicRouteTarget)
	for _, target := range targets {
		targetsByRoute[target.RouteID] = append(targetsByRoute[target.RouteID], target)
	}
	for _, route := range routes {
		if route.SiteID.Valid && route.SiteID.Int64 == site.ID && route.Enabled != 0 {
			enabledRoute = true
			if route.Action == publicRouteActionForward {
				hasEnabledTarget := false
				for _, target := range targetsByRoute[route.ID] {
					hasEnabledTarget = hasEnabledTarget || target.Enabled != 0
				}
				if !hasEnabledTarget {
					blocker("site.route.targets.required", "Enabled forward route has no enabled target", "edit_route", 0, route.ID, "")
				} else {
					unknown("site.backend.not_checked", "Backend reachability is not checked", "edit_route", 0, route.ID)
				}
			}
		}
	}
	if !enabledRoute {
		warning("site.routes.empty", "This Site has no enabled routes", "add_route", 0)
	}
	return items
}

func publicListenerHasUsableTLSCertificate(listenerID int64, certs []db.PublicTlsCertificate) bool {
	now := time.Now()
	for _, cert := range certs {
		if cert.ListenerID != listenerID || cert.Enabled == 0 || cert.CertPath == "" || cert.KeyPath == "" {
			continue
		}
		pair, err := tls.LoadX509KeyPair(cert.CertPath, cert.KeyPath)
		if err != nil || len(pair.Certificate) == 0 {
			continue
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err == nil && !now.Before(leaf.NotBefore) && !now.After(leaf.NotAfter) {
			return true
		}
	}
	return false
}

func validatePublicSiteRedirectBindingReadiness(items *[]*p2pstreamv1.PublicSiteReadinessItem, site db.PublicSite, hosts []db.PublicSiteHost, binding db.PublicSiteListenerBinding, bindings []db.PublicSiteListenerBinding, allSites []db.PublicSite, hostsBySite map[int64][]db.PublicSiteHost, bindingsBySite map[int64][]db.PublicSiteListenerBinding, listeners map[int64]db.PublicListener) {
	add := func(code, message, action string, hostname string) {
		*items = append(*items, &p2pstreamv1.PublicSiteReadinessItem{Code: code, Severity: p2pstreamv1.PublicSiteReadinessSeverity_PUBLIC_SITE_READINESS_SEVERITY_BLOCKER, Message: message, ListenerId: binding.ListenerID, Hostname: hostname, Action: action})
	}
	if source, exists := listeners[binding.ListenerID]; !exists || source.Protocol != publicListenerProtocolHTTP {
		add("site.redirect.source_http", "Redirect to HTTPS is only available on an HTTP listener", "edit_listener", "")
		return
	}
	if !binding.RedirectListenerID.Valid {
		add("site.redirect.listener.required", "Choose an HTTPS destination listener", "edit_listener", "")
		return
	}
	if binding.RedirectListenerID.Int64 == binding.ListenerID {
		add("site.redirect.loop", "A listener cannot redirect to itself", "edit_listener", binding.RedirectHostname)
		return
	}
	destination, exists := listeners[binding.RedirectListenerID.Int64]
	if !exists || destination.Protocol != publicListenerProtocolHTTPS {
		add("site.redirect.https", "Redirect destination must be an HTTPS listener", "edit_listener", binding.RedirectHostname)
		return
	}
	hasServeDestination := false
	for _, candidate := range bindings {
		if candidate.ListenerID == destination.ID && candidate.Behavior == publicSiteListenerBehaviorServe {
			hasServeDestination = true
			break
		}
	}
	if !hasServeDestination {
		add("site.redirect.destination_binding", "Redirect destination must Serve this Site", "edit_listener", binding.RedirectHostname)
	}
	if site.DefaultSite != 0 && binding.RedirectHostname == "" {
		add("site.redirect.hostname.required", "Default Site redirects require an exact destination hostname", "edit_listener", "")
		return
	}
	if binding.RedirectHostname == "" {
		for _, hostname := range publicSiteRedirectOwnershipProbes(site.ID, hosts, allSites, hostsBySite) {
			sourceSiteID := resolvePublishedNamedSiteID(binding.ListenerID, hostname, allSites, hostsBySite, bindingsBySite)
			destinationSiteID := resolvePublishedNamedSiteID(destination.ID, hostname, allSites, hostsBySite, bindingsBySite)
			if sourceSiteID == site.ID && destinationSiteID != site.ID {
				add("site.redirect.hostname.shadowed", fmt.Sprintf("Hostname %q would redirect into another Site on the destination listener", hostname), "edit_hostnames", hostname)
				return
			}
		}
		return
	}
	if site.DefaultSite == 0 {
		if resolvePublishedNamedSiteID(destination.ID, binding.RedirectHostname, allSites, hostsBySite, bindingsBySite) != site.ID {
			add("site.redirect.hostname.ownership", "Redirect hostname must resolve to this Site on the destination listener", "edit_listener", binding.RedirectHostname)
		}
		return
	}
	for _, other := range allSites {
		if other.ID == site.ID || other.Published == 0 || other.DefaultSite != 0 {
			continue
		}
		bound := false
		for _, otherBinding := range bindingsBySite[other.ID] {
			bound = bound || otherBinding.ListenerID == destination.ID
		}
		if !bound {
			continue
		}
		for _, host := range hostsBySite[other.ID] {
			if strictPublicSiteHostMatches(binding.RedirectHostname, host.HostnamePattern) {
				add("site.redirect.hostname.claimed", "Redirect hostname is claimed by another Site on the destination listener", "edit_listener", binding.RedirectHostname)
				return
			}
		}
	}
}

func resolvePublishedNamedSiteID(listenerID int64, hostname string, sites []db.PublicSite, hostsBySite map[int64][]db.PublicSiteHost, bindingsBySite map[int64][]db.PublicSiteListenerBinding) int64 {
	hostname = normalizeHostPattern(hostname)
	sitesByID := make(map[int64]db.PublicSite, len(sites))
	for _, site := range sites {
		sitesByID[site.ID] = site
	}
	bestSiteID, bestSpecificity := int64(0), -1
	for siteID, hosts := range hostsBySite {
		site := sitesByID[siteID]
		if site.Published == 0 || site.DefaultSite != 0 {
			continue
		}
		bound := false
		for _, candidate := range bindingsBySite[siteID] {
			bound = bound || candidate.ListenerID == listenerID
		}
		if !bound {
			continue
		}
		for _, host := range hosts {
			pattern := normalizeHostPattern(host.HostnamePattern)
			if !strictPublicSiteHostMatches(hostname, pattern) {
				continue
			}
			specificity := len(pattern)
			if !strings.HasPrefix(pattern, "*.") {
				specificity += 1 << 20
			}
			if specificity > bestSpecificity {
				bestSiteID, bestSpecificity = siteID, specificity
			}
		}
	}
	return bestSiteID
}

func publicSiteRedirectOwnershipProbes(siteID int64, hosts []db.PublicSiteHost, allSites []db.PublicSite, hostsBySite map[int64][]db.PublicSiteHost) []string {
	seen := make(map[string]struct{})
	add := func(hostname string) {
		if hostname != "" && !strings.HasPrefix(hostname, "*.") {
			seen[hostname] = struct{}{}
		}
	}
	for _, own := range hosts {
		if strings.HasPrefix(own.HostnamePattern, "*.") {
			add("site-probe." + strings.TrimPrefix(own.HostnamePattern, "*."))
		} else {
			add(own.HostnamePattern)
		}
		for otherSiteID, otherHosts := range hostsBySite {
			if otherSiteID == siteID {
				continue
			}
			for _, other := range otherHosts {
				if strings.HasPrefix(other.HostnamePattern, "*.") {
					probe := "site-probe." + strings.TrimPrefix(other.HostnamePattern, "*.")
					if strictPublicSiteHostMatches(probe, own.HostnamePattern) {
						add(probe)
					}
				} else if strictPublicSiteHostMatches(other.HostnamePattern, own.HostnamePattern) {
					add(other.HostnamePattern)
				}
			}
		}
	}
	probes := make([]string, 0, len(seen))
	for hostname := range seen {
		probes = append(probes, hostname)
	}
	sort.Strings(probes)
	return probes
}

func (a *App) CreatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicSiteRequest]) (*connect.Response[p2pstreamv1.CreatePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().createPublicSite(ctx, req)
}

func (s *publicConfigService) createPublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicSiteRequest]) (*connect.Response[p2pstreamv1.CreatePublicSiteResponse], error) {
	name, err := normalizePublicName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	hosts, err := validatePublicSiteHosts(req.Msg.Hosts)
	if err != nil {
		return nil, err
	}
	canonical, err := validatePublicSiteCanonicalHostname(req.Msg.CanonicalHostname, hosts)
	if err != nil {
		return nil, err
	}
	mirrorPublicSiteCanonicalRole(hosts, canonical)
	bindings, err := validatePublicSiteListenerBindings(ctx, s.db.Queries, req.Msg.ListenerId, req.Msg.ListenerBindings)
	if err != nil {
		return nil, err
	}
	if err := s.app.ensurePublicConfigCandidatePrerequisites(ctx); err != nil {
		return nil, err
	}
	s.app.publicConfigRefreshMu.Lock()
	defer s.app.publicConfigRefreshMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	site, err := q.CreatePublicSite(ctx, db.CreatePublicSiteParams{
		Name: name, Enabled: boolInt(req.Msg.Enabled), Published: 0,
		DefaultSite: boolInt(req.Msg.DefaultSite), CanonicalHostname: canonical,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if _, err := replacePublicSiteHosts(ctx, q, site.ID, hosts); err != nil {
		return nil, publicDBError(err)
	}
	if _, err := replacePublicSiteListenerBindings(ctx, q, site.ID, bindings); err != nil {
		return nil, publicDBError(err)
	}
	rows, snapshot, err := preparePublicConfigCandidateTx(ctx, q)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	s.app.applyPreparedPublicConfigCandidate(rows, snapshot)
	result, err := s.publicSiteProto(ctx, site.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicSiteResponse{Site: result}), nil
}

func (a *App) UpdatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicSiteRequest]) (*connect.Response[p2pstreamv1.UpdatePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().updatePublicSite(ctx, req)
}

func (s *publicConfigService) updatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicSiteRequest]) (*connect.Response[p2pstreamv1.UpdatePublicSiteResponse], error) {
	name, err := normalizePublicName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	hosts, err := validatePublicSiteHosts(req.Msg.Hosts)
	if err != nil {
		return nil, err
	}
	canonical, err := validatePublicSiteCanonicalHostname(req.Msg.CanonicalHostname, hosts)
	if err != nil {
		return nil, err
	}
	mirrorPublicSiteCanonicalRole(hosts, canonical)
	bindings, err := validatePublicSiteListenerBindings(ctx, s.db.Queries, req.Msg.ListenerId, req.Msg.ListenerBindings)
	if err != nil {
		return nil, err
	}
	if err := s.app.ensurePublicConfigCandidatePrerequisites(ctx); err != nil {
		return nil, err
	}
	s.app.publicConfigRefreshMu.Lock()
	defer s.app.publicConfigRefreshMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	existing, err := q.GetPublicSite(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if len(req.Msg.ListenerBindings) == 0 && req.Msg.ListenerId > 0 {
		existingBindings, err := q.ListPublicSiteListenerBindingsBySite(ctx, req.Msg.Id)
		if err != nil {
			return nil, publicDBError(err)
		}
		if len(existingBindings) > 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this Site uses multiple listeners; update it with a client that supports listener bindings"))
		}
	}
	site, err := q.UpdatePublicSite(ctx, db.UpdatePublicSiteParams{
		Name: name, Enabled: boolInt(req.Msg.Enabled), DefaultSite: boolInt(req.Msg.DefaultSite),
		CanonicalHostname: canonical, ID: req.Msg.Id,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if _, err := replacePublicSiteHosts(ctx, q, site.ID, hosts); err != nil {
		return nil, publicDBError(err)
	}
	if _, err := replacePublicSiteListenerBindings(ctx, q, site.ID, bindings); err != nil {
		return nil, publicDBError(err)
	}
	if err := q.UpdatePublicRouteHostPatternBySite(ctx, db.UpdatePublicRouteHostPatternBySiteParams{HostPattern: "", SiteID: sql.NullInt64{Int64: site.ID, Valid: true}}); err != nil {
		return nil, publicDBError(err)
	}
	if existing.Published != 0 {
		if err := validatePublishedSitesTx(ctx, q, site.ID, false); err != nil {
			return nil, err
		}
	}
	rows, snapshot, err := preparePublicConfigCandidateTx(ctx, q)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	s.app.applyPreparedPublicConfigCandidate(rows, snapshot)
	result, err := s.publicSiteProto(ctx, site.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicSiteResponse{Site: result}), nil
}

func (a *App) PublishPublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.PublishPublicSiteRequest]) (*connect.Response[p2pstreamv1.PublishPublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().publishPublicSite(ctx, req)
}

func (s *publicConfigService) publishPublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.PublishPublicSiteRequest]) (*connect.Response[p2pstreamv1.PublishPublicSiteResponse], error) {
	if err := s.app.ensurePublicConfigCandidatePrerequisites(ctx); err != nil {
		return nil, err
	}
	s.app.publicConfigRefreshMu.Lock()
	defer s.app.publicConfigRefreshMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	if _, err := q.GetPublicSite(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	bindings, err := q.ListPublicSiteListenerBindingsBySite(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if len(bindings) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assign at least one listener before publishing"))
	}
	if _, err := q.SetPublicSitePublished(ctx, db.SetPublicSitePublishedParams{Published: 1, ID: req.Msg.Id}); err != nil {
		return nil, publicDBError(err)
	}
	if err := validatePublishedSitesTx(ctx, q, req.Msg.Id, true); err != nil {
		return nil, err
	}
	rows, snapshot, err := preparePublicConfigCandidateTx(ctx, q)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	s.app.applyPreparedPublicConfigCandidate(rows, snapshot)
	result, err := s.publicSiteProto(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.PublishPublicSiteResponse{Site: result}), nil
}

func replacePublicSiteHosts(ctx context.Context, q *db.Queries, siteID int64, hosts []publicSiteHostMutation) ([]db.PublicSiteHost, error) {
	if err := q.DeletePublicSiteHosts(ctx, siteID); err != nil {
		return nil, err
	}
	stored := make([]db.PublicSiteHost, 0, len(hosts))
	for _, host := range hosts {
		row, err := q.CreatePublicSiteHost(ctx, db.CreatePublicSiteHostParams{SiteID: siteID, HostnamePattern: host.HostnamePattern, Role: host.Role, Behavior: host.Behavior})
		if err != nil {
			return nil, err
		}
		stored = append(stored, row)
	}
	return stored, nil
}

func replacePublicSiteListenerBindings(ctx context.Context, q *db.Queries, siteID int64, bindings []publicSiteListenerBindingMutation) ([]db.PublicSiteListenerBinding, error) {
	if err := q.DeletePublicSiteListenerBindings(ctx, siteID); err != nil {
		return nil, err
	}
	stored := make([]db.PublicSiteListenerBinding, 0, len(bindings))
	for _, binding := range bindings {
		row, err := q.CreatePublicSiteListenerBinding(ctx, db.CreatePublicSiteListenerBindingParams{
			SiteID: siteID, ListenerID: binding.ListenerID, Behavior: binding.Behavior,
			RedirectListenerID: binding.RedirectListenerID, RedirectHostname: binding.RedirectHostname,
		})
		if err != nil {
			return nil, err
		}
		stored = append(stored, row)
	}
	return stored, nil
}

func (a *App) DeletePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicSiteRequest]) (*connect.Response[p2pstreamv1.DeletePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().deletePublicSite(ctx, req)
}

func (s *publicConfigService) deletePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicSiteRequest]) (*connect.Response[p2pstreamv1.DeletePublicSiteResponse], error) {
	if err := s.app.ensurePublicConfigCandidatePrerequisites(ctx); err != nil {
		return nil, err
	}
	s.app.publicConfigRefreshMu.Lock()
	defer s.app.publicConfigRefreshMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	if _, err := q.GetPublicSite(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	count, err := q.CountPublicRoutesBySite(ctx, sql.NullInt64{Int64: req.Msg.Id, Valid: true})
	if err != nil {
		return nil, publicDBError(err)
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("delete or detach the site's routes first"))
	}
	if err := q.DeletePublicSite(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := validatePublishedSitesTx(ctx, q, 0, false); err != nil {
		return nil, err
	}
	rows, snapshot, err := preparePublicConfigCandidateTx(ctx, q)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	s.app.applyPreparedPublicConfigCandidate(rows, snapshot)
	return connect.NewResponse(&p2pstreamv1.DeletePublicSiteResponse{}), nil
}

func publicSiteToProto(site db.PublicSite, hosts []db.PublicSiteHost, bindings []db.PublicSiteListenerBinding, listeners map[int64]db.PublicListener, certs []db.PublicTlsCertificate, readiness []*p2pstreamv1.PublicSiteReadinessItem) *p2pstreamv1.PublicSite {
	result := &p2pstreamv1.PublicSite{Id: site.ID, Name: site.Name, Enabled: site.Enabled != 0, Published: site.Published != 0, DefaultSite: site.DefaultSite != 0, CanonicalHostname: site.CanonicalHostname, CreatedAtUnixMillis: site.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: site.UpdatedAt.UnixMilli(), Readiness: readiness}
	protocol, compatibilityListenerID := "", int64(0)
	for _, binding := range bindings {
		behavior := p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE
		if binding.Behavior == publicSiteListenerBehaviorRedirectHTTPS {
			behavior = p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS
		}
		result.ListenerBindings = append(result.ListenerBindings, &p2pstreamv1.PublicSiteListenerBinding{ListenerId: binding.ListenerID, Behavior: behavior, RedirectListenerId: nullInt64Value(binding.RedirectListenerID), RedirectHostname: binding.RedirectHostname})
		if compatibilityListenerID == 0 || binding.ListenerID < compatibilityListenerID {
			compatibilityListenerID = binding.ListenerID
		}
	}
	result.ListenerId = compatibilityListenerID
	if listener, ok := listeners[compatibilityListenerID]; ok {
		protocol = listener.Protocol
	}
	for _, host := range hosts {
		coverage, detail := publicSiteTLSCoverage(protocol, compatibilityListenerID, host.HostnamePattern, certs)
		behavior := p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE
		if host.Behavior == publicSiteHostBehaviorRedirect {
			behavior = p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT
		}
		result.Hosts = append(result.Hosts, &p2pstreamv1.PublicSiteHost{Id: host.ID, SiteId: host.SiteID, HostnamePattern: host.HostnamePattern, Primary: host.Role == publicSiteHostRolePrimary, Behavior: behavior, TlsCoverage: coverage, TlsDetail: detail, CreatedAtUnixMillis: host.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: host.UpdatedAt.UnixMilli()})
	}
	return result
}

func publicSitesToProto(sites []db.PublicSite, hosts []db.PublicSiteHost, bindings []db.PublicSiteListenerBinding, listeners []db.PublicListener, certs []db.PublicTlsCertificate, routes []db.PublicRoute, targets []db.PublicRouteTarget) []*p2pstreamv1.PublicSite {
	hostsBySite := make(map[int64][]db.PublicSiteHost)
	for _, host := range hosts {
		hostsBySite[host.SiteID] = append(hostsBySite[host.SiteID], host)
	}
	listenersByID := make(map[int64]db.PublicListener)
	for _, listener := range listeners {
		listenersByID[listener.ID] = listener
	}
	bindingsBySite := make(map[int64][]db.PublicSiteListenerBinding)
	for _, binding := range bindings {
		bindingsBySite[binding.SiteID] = append(bindingsBySite[binding.SiteID], binding)
	}
	result := make([]*p2pstreamv1.PublicSite, 0, len(sites))
	for _, site := range sites {
		readiness := buildPublicSiteReadiness(site, hostsBySite[site.ID], bindingsBySite[site.ID], sites, hosts, bindings, listeners, certs, routes, targets)
		result = append(result, publicSiteToProto(site, hostsBySite[site.ID], bindingsBySite[site.ID], listenersByID, certs, readiness))
	}
	return result
}

func publicSiteTLSCoverage(protocol string, listenerID int64, hostname string, certs []db.PublicTlsCertificate) (p2pstreamv1.PublicSiteTlsCoverage, string) {
	if protocol != publicListenerProtocolHTTPS {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_NOT_APPLICABLE, "HTTP listener"
	}
	if addr, err := netip.ParseAddr(hostname); err == nil && addr.Zone() == "" {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING, "IP hosts cannot select an HTTPS certificate with SNI; use a DNS hostname"
	}
	var exact *db.PublicTlsCertificate
	var wildcards []*db.PublicTlsCertificate
	var unavailableMatches []string
	for i := range certs {
		certRow := &certs[i]
		if certRow.ListenerID != listenerID || certRow.Enabled == 0 {
			continue
		}
		pattern := canonicalPublicHostnamePatternOrLegacy(certRow.HostnamePattern)
		if !strictPublicSiteHostMatches(hostname, pattern) {
			continue
		}
		if normalizePublicTLSCertificateSource(certRow.Source) == publicTLSCertificateSourceACME && (certRow.CertPath == "" || certRow.KeyPath == "") {
			unavailableMatches = append(unavailableMatches, pattern)
			continue
		}
		if normalizePublicTLSCertificateSource(certRow.Source) == publicTLSCertificateSourceACME {
			if _, err := os.Stat(certRow.CertPath); errors.Is(err, os.ErrNotExist) {
				unavailableMatches = append(unavailableMatches, pattern)
				continue
			}
			if _, err := os.Stat(certRow.KeyPath); errors.Is(err, os.ErrNotExist) {
				unavailableMatches = append(unavailableMatches, pattern)
				continue
			}
		}
		if strings.HasPrefix(pattern, "*.") {
			wildcards = append(wildcards, certRow)
			continue
		}
		// The live selector builds an exact-name map in query order, so the
		// last duplicate mapping wins. Preserve that behavior here.
		exact = certRow
	}
	// The live selector stable-sorts wildcards by specificity. Equal patterns
	// therefore retain query order (listener, pattern, ID), making the first
	// duplicate mapping effective.
	sort.SliceStable(wildcards, func(i, j int) bool {
		return len(normalizeHostPattern(wildcards[i].HostnamePattern)) > len(normalizeHostPattern(wildcards[j].HostnamePattern))
	})
	selected := exact
	if selected == nil && len(wildcards) > 0 {
		selected = wildcards[0]
	}
	if selected == nil {
		if len(unavailableMatches) > 0 {
			return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID,
				fmt.Sprintf("certificate mapping %q covers %q, but issuance has not produced usable certificate material", unavailableMatches[len(unavailableMatches)-1], hostname)
		}
		for i := range certs {
			certRow := &certs[i]
			if certRow.ListenerID != listenerID || certRow.Enabled == 0 {
				continue
			}
			pattern := canonicalPublicHostnamePatternOrLegacy(certRow.HostnamePattern)
			if strings.HasPrefix(pattern, "*.") && hostname == strings.TrimPrefix(pattern, "*.") {
				return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING,
					fmt.Sprintf("%q is an apex hostname; wildcard mapping %q covers one-label subdomains but not the apex", hostname, pattern)
			}
		}
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING, fmt.Sprintf("no enabled certificate mapping covers %q", hostname)
	}
	pair, err := tls.LoadX509KeyPair(selected.CertPath, selected.KeyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate material is unavailable"
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate is outside its validity period"
	}
	if strings.HasPrefix(hostname, "*.") {
		for _, dnsName := range leaf.DNSNames {
			normalized, normErr := normalizePublicSiteHostnamePattern(dnsName)
			if normErr == nil && normalized == hostname {
				return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED, "certificate SAN covers wildcard"
			}
		}
	} else if leaf.VerifyHostname(hostname) == nil {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED, "certificate SAN covers hostname"
	}
	return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate SAN does not cover hostname"
}

// strictPublicSiteHostMatches uses X.509-style one-label wildcard semantics.
// It is deliberately separate from the legacy route and policy matcher.
func strictPublicSiteHostMatches(hostname, pattern string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	pattern = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
	if !strings.HasPrefix(pattern, "*.") {
		return hostname == pattern
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	if !strings.HasSuffix(hostname, "."+suffix) {
		return false
	}
	prefix := strings.TrimSuffix(hostname, "."+suffix)
	return prefix != "" && !strings.Contains(prefix, ".")
}
