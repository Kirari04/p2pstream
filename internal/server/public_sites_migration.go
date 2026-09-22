package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	publicSiteMigrationWarningAuthority = "strict_authority_parsing"
	publicSiteMigrationWarningSNI       = "https_sni_authority_match"
)

type publicSiteMigrationListener struct {
	ID       int64
	Name     string
	Protocol string
}

type publicSiteMigrationRoute struct {
	ID          int64
	ListenerID  int64
	Priority    int64
	HostPattern string
	PathPrefix  string
	Action      string
	IsDefault   bool
	Enabled     bool
}

type publicSiteMigrationClaim struct {
	SiteID          int64
	ListenerID      int64
	DefaultSite     bool
	HostnamePattern string
}

type publicSiteMigrationState struct {
	Listeners []publicSiteMigrationListener
	Routes    []publicSiteMigrationRoute
	Claims    []publicSiteMigrationClaim
	Certs     []db.PublicTlsCertificate
	Targets   []db.PublicRouteTarget
	Revision  string
}

type publicSiteMigrationPlan struct {
	Revision string
	Groups   []*p2pstreamv1.PublicSiteMigrationGroup
	Issues   []*p2pstreamv1.PublicSiteMigrationIssue
	Routes   map[int64]publicSiteMigrationRoute
}

func (a *App) PreviewPublicSiteMigration(ctx context.Context, req *connect.Request[p2pstreamv1.PreviewPublicSiteMigrationRequest]) (*connect.Response[p2pstreamv1.PreviewPublicSiteMigrationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().previewPublicSiteMigration(ctx, req.Msg.ListenerIds)
}

func (s *publicConfigService) previewPublicSiteMigration(ctx context.Context, listenerIDs []int64) (*connect.Response[p2pstreamv1.PreviewPublicSiteMigrationResponse], error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	state, err := loadPublicSiteMigrationState(ctx, tx, listenerIDs)
	if err != nil {
		return nil, publicSiteMigrationError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	plan := planPublicSiteMigration(state)
	return connect.NewResponse(publicSiteMigrationPreviewProto(plan)), nil
}

func (a *App) ApplyPublicSiteMigration(ctx context.Context, req *connect.Request[p2pstreamv1.ApplyPublicSiteMigrationRequest]) (*connect.Response[p2pstreamv1.ApplyPublicSiteMigrationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().applyPublicSiteMigration(ctx, req.Msg)
}

func (s *publicConfigService) applyPublicSiteMigration(ctx context.Context, request *p2pstreamv1.ApplyPublicSiteMigrationRequest) (*connect.Response[p2pstreamv1.ApplyPublicSiteMigrationResponse], error) {
	if strings.TrimSpace(request.Revision) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("migration revision is required"))
	}
	if err := s.app.ensurePublicProxySeeded(ctx); err != nil {
		return nil, err
	}
	if _, err := s.app.ensurePublicWafSettings(ctx); err != nil {
		return nil, err
	}
	if _, err := s.app.ensurePublicGeoIPSettings(ctx); err != nil {
		return nil, err
	}
	if _, err := s.app.ensurePublicCacheSettings(ctx); err != nil {
		return nil, err
	}
	s.app.publicConfigRefreshMu.Lock()
	defer s.app.publicConfigRefreshMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()

	state, err := loadPublicSiteMigrationState(ctx, tx, request.ListenerIds)
	if err != nil {
		return nil, publicSiteMigrationError(err)
	}
	plan := planPublicSiteMigration(state)
	if request.Revision != plan.Revision {
		return nil, connect.NewError(connect.CodeAborted, errors.New("migration preview is stale; generate a new preview"))
	}
	for _, issue := range plan.Issues {
		if issue.Severity == p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_BLOCKER {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("migration preview contains blocking issues"))
		}
	}
	accepted := make(map[string]struct{}, len(request.AcceptedWarningCodes))
	for _, code := range request.AcceptedWarningCodes {
		accepted[strings.TrimSpace(code)] = struct{}{}
	}
	for _, issue := range plan.Issues {
		if !issue.RequiresAcknowledgement {
			continue
		}
		if _, ok := accepted[issue.Code]; !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("migration warning %q must be acknowledged", issue.Code))
		}
	}

	created, mappings, err := applyPublicSiteMigrationPlan(ctx, tx, plan)
	if err != nil {
		return nil, publicSiteMigrationError(err)
	}
	q := s.db.Queries.WithTx(tx)
	for _, listener := range state.Listeners {
		if err := q.MarkPublicListenerSiteMigrated(ctx, listener.ID); err != nil {
			return nil, publicSiteMigrationError(err)
		}
	}
	for _, site := range created {
		if err := validatePublishedSitesTx(ctx, q, site.Id, true); err != nil {
			return nil, publicSiteMigrationError(err)
		}
		readiness, err := validatePublishedSiteTx(ctx, q, site.Id)
		if err != nil {
			return nil, publicSiteMigrationError(err)
		}
		if err := publicSiteReadinessBlocker(readiness); err != nil {
			return nil, err
		}
		site.Readiness = readiness
	}
	if err := validatePublishedSitesTx(ctx, q, 0, false); err != nil {
		return nil, publicSiteMigrationError(err)
	}
	candidateRows, candidateSnapshot, err := preparePublicConfigCandidateTx(ctx, q)
	if err != nil {
		return nil, publicSiteMigrationError(err)
	}
	postState, err := loadPublicSiteMigrationState(ctx, tx, request.ListenerIds)
	if err != nil {
		return nil, publicSiteMigrationError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	s.app.applyPreparedPublicConfigCandidate(candidateRows, candidateSnapshot)
	return connect.NewResponse(&p2pstreamv1.ApplyPublicSiteMigrationResponse{
		Revision:     postState.Revision,
		CreatedSites: created,
		Mappings:     mappings,
	}), nil
}

func publicSiteMigrationPreviewProto(plan publicSiteMigrationPlan) *p2pstreamv1.PreviewPublicSiteMigrationResponse {
	canApply := true
	for _, issue := range plan.Issues {
		if issue.Severity == p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_BLOCKER {
			canApply = false
			break
		}
	}
	return &p2pstreamv1.PreviewPublicSiteMigrationResponse{Revision: plan.Revision, Groups: plan.Groups, Issues: plan.Issues, CanApply: canApply}
}

func planPublicSiteMigration(state publicSiteMigrationState) publicSiteMigrationPlan {
	plan := publicSiteMigrationPlan{Revision: state.Revision, Routes: make(map[int64]publicSiteMigrationRoute, len(state.Routes))}
	listeners := make(map[int64]publicSiteMigrationListener, len(state.Listeners))
	for _, listener := range state.Listeners {
		listeners[listener.ID] = listener
	}
	routesByListener := make(map[int64][]publicSiteMigrationRoute)
	for _, route := range state.Routes {
		plan.Routes[route.ID] = route
		routesByListener[route.ListenerID] = append(routesByListener[route.ListenerID], route)
	}
	claimsByListener := make(map[int64][]publicSiteMigrationClaim)
	for _, claim := range state.Claims {
		claimsByListener[claim.ListenerID] = append(claimsByListener[claim.ListenerID], claim)
	}
	enabledTargetsByRoute := make(map[int64]int)
	for _, target := range state.Targets {
		if target.Enabled != 0 {
			enabledTargetsByRoute[target.RouteID]++
		}
	}

	for _, listener := range state.Listeners {
		routes := routesByListener[listener.ID]
		if len(routes) == 0 {
			continue
		}
		groupStart := len(plan.Groups)
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Priority == routes[j].Priority {
				return routes[i].ID < routes[j].ID
			}
			return routes[i].Priority < routes[j].Priority
		})
		var generic []publicSiteMigrationRoute
		exact := make(map[string][]publicSiteMigrationRoute)
		for _, route := range routes {
			if route.Enabled && route.Action == publicRouteActionForward && enabledTargetsByRoute[route.ID] == 0 {
				plan.Issues = append(plan.Issues, migrationBlocker("enabled_forward_without_target", listener.ID, []int64{route.ID}, "Enabled forward route has no enabled target", "Add or enable a target, disable the route, or change its action before migration."))
			}
			pattern := normalizeHostPattern(route.HostPattern)
			if route.IsDefault || pattern == "" {
				generic = append(generic, route)
				continue
			}
			if strings.HasPrefix(pattern, "*.") {
				plan.Issues = append(plan.Issues, migrationBlocker("legacy_wildcard_semantics", listener.ID, []int64{route.ID}, "Wildcard route needs review", "Legacy wildcards match any subdomain depth, while Site wildcards match one label. Replace or split this route before migration."))
				continue
			}
			normalized, err := normalizePublicSiteHostnamePattern(pattern)
			if err != nil || normalized != pattern {
				plan.Issues = append(plan.Issues, migrationBlocker("hostname_canonicalization_change", listener.ID, []int64{route.ID}, "Hostname cannot be migrated exactly", "The stored route hostname is not already in the canonical Site hostname form. Normalize it and review matching behavior before migration."))
				continue
			}
			if addressIsHTTPSIncompatible(listener.Protocol, normalized) {
				plan.Issues = append(plan.Issues, migrationBlocker("https_ip_hostname", listener.ID, []int64{route.ID}, "HTTPS IP hostname cannot become a Site", "HTTPS Site selection requires DNS SNI. Use a DNS hostname or keep this listener unmigrated."))
				continue
			}
			exact[normalized] = append(exact[normalized], route)
		}

		hosts := make([]string, 0, len(exact))
		for host := range exact {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts {
			var conflicts []int64
			for _, claim := range claimsByListener[listener.ID] {
				if claim.DefaultSite || claim.HostnamePattern == "" {
					continue
				}
				if strictPublicSiteHostMatches(host, claim.HostnamePattern) || strictPublicSiteHostMatches(claim.HostnamePattern, host) {
					conflicts = append(conflicts, claim.SiteID)
				}
			}
			if len(conflicts) > 0 {
				plan.Issues = append(plan.Issues, migrationBlocker("existing_site_hostname_overlap", listener.ID, routeIDs(exact[host]), "Hostname is already owned by a Site", fmt.Sprintf("%s overlaps an existing published Site binding. Resolve that ownership before migration.", host)))
				continue
			}
			if listener.Protocol == publicListenerProtocolHTTPS {
				coverage, detail := publicSiteTLSCoverage(listener.Protocol, listener.ID, host, state.Certs)
				if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED {
					plan.Issues = append(plan.Issues, migrationBlocker("https_certificate_coverage", listener.ID, routeIDs(exact[host]), "HTTPS hostname lacks valid certificate coverage", detail+". Configure the effective TLS mapping before migration."))
				}
			}
			for _, specific := range exact[host] {
				for _, fallback := range generic {
					if specific.Priority == fallback.Priority && !specific.IsDefault && !fallback.IsDefault {
						plan.Issues = append(plan.Issues, migrationBlocker("copy_priority_tie", listener.ID, []int64{specific.ID, fallback.ID}, "Fallback copy would change tie ordering", "These routes share a priority and currently use route IDs as the tie-breaker. Give them distinct priorities before migration."))
					}
				}
			}
			group := &p2pstreamv1.PublicSiteMigrationGroup{
				Key:               publicSiteMigrationGroupKey(listener.ID, host),
				ListenerId:        listener.ID,
				ListenerName:      listener.Name,
				ProposedSiteName:  publicSiteMigrationName(listener.ID, host),
				HostnameMode:      p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_SPECIFIC,
				HostnamePatterns:  []string{host},
				SourceRouteIds:    routeIDs(exact[host]),
				PreservesRouteIds: len(generic) == 0,
			}
			for _, route := range generic {
				group.RouteCopies = append(group.RouteCopies, &p2pstreamv1.PublicSiteMigrationRouteCopy{SourceRouteId: route.ID, DestinationGroupKey: group.Key, Reason: "preserve listener-wide fallback for this hostname"})
			}
			plan.Groups = append(plan.Groups, group)
		}

		if len(generic) > 0 {
			for _, claim := range claimsByListener[listener.ID] {
				if claim.DefaultSite {
					plan.Issues = append(plan.Issues, migrationBlocker("existing_default_site", listener.ID, routeIDs(generic), "Listener already has a Default Site", "Move or reconcile the listener-wide routes with the existing Default Site before migration."))
					break
				}
			}
			plan.Groups = append(plan.Groups, &p2pstreamv1.PublicSiteMigrationGroup{
				Key:               publicSiteMigrationGroupKey(listener.ID, ""),
				ListenerId:        listener.ID,
				ListenerName:      listener.Name,
				ProposedSiteName:  publicSiteMigrationName(listener.ID, "default"),
				HostnameMode:      p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_DEFAULT,
				SourceRouteIds:    routeIDs(generic),
				PreservesRouteIds: true,
			})
		}
		if len(plan.Groups) > groupStart {
			ids := routeIDs(routes)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, group := range plan.Groups[groupStart:] {
				hasEnabled := false
				groupRouteIDs := append([]int64(nil), group.SourceRouteIds...)
				for _, copySpec := range group.RouteCopies {
					groupRouteIDs = append(groupRouteIDs, copySpec.SourceRouteId)
				}
				for _, routeID := range groupRouteIDs {
					hasEnabled = hasEnabled || plan.Routes[routeID].Enabled
				}
				if !hasEnabled {
					plan.Issues = append(plan.Issues, migrationNotice("site_no_enabled_routes", listener.ID, groupRouteIDs, "Migrated Site will have no enabled routes", "The published Site will retain ownership but return 404 until one of its routes is enabled."))
				}
			}
			if len(hosts) > 0 && len(generic) > 0 {
				plan.Issues = append(plan.Issues, migrationNotice("fallback_routes_copied", listener.ID, routeIDs(generic), "Listener fallbacks will be copied", "Listener-wide path and default fallbacks will be copied into each new named Site to preserve path-miss behavior. Each copy becomes independently editable after migration."))
			}
			plan.Issues = append(plan.Issues, migrationWarning(publicSiteMigrationWarningAuthority, listener.ID, ids, "Site authority parsing is stricter", "Sites reject malformed authorities and canonicalize DNS names before routing. Acknowledge this intentional hardening to apply the reviewed migration."))
			if listener.Protocol == publicListenerProtocolHTTPS {
				plan.Issues = append(plan.Issues, migrationWarning(publicSiteMigrationWarningSNI, listener.ID, ids, "HTTPS Sites require matching SNI and authority", "DNS requests selected through a named or Default Site require TLS SNI and HTTP authority to be present and equal; mismatches return 421. IP-literal authority keeps its no-SNI exception. Acknowledge this intentional hardening to apply the reviewed migration."))
			}
		}
	}
	sort.SliceStable(plan.Groups, func(i, j int) bool {
		if plan.Groups[i].ListenerId == plan.Groups[j].ListenerId {
			if plan.Groups[i].HostnameMode != plan.Groups[j].HostnameMode {
				return plan.Groups[i].HostnameMode == p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_SPECIFIC
			}
			return plan.Groups[i].Key < plan.Groups[j].Key
		}
		return plan.Groups[i].ListenerId < plan.Groups[j].ListenerId
	})
	sort.SliceStable(plan.Issues, func(i, j int) bool {
		if plan.Issues[i].ListenerId == plan.Issues[j].ListenerId {
			if plan.Issues[i].Severity == plan.Issues[j].Severity {
				return plan.Issues[i].Code < plan.Issues[j].Code
			}
			return plan.Issues[i].Severity > plan.Issues[j].Severity
		}
		return plan.Issues[i].ListenerId < plan.Issues[j].ListenerId
	})
	return plan
}

func migrationBlocker(code string, listenerID int64, routeIDs []int64, summary, detail string) *p2pstreamv1.PublicSiteMigrationIssue {
	return &p2pstreamv1.PublicSiteMigrationIssue{Code: code, Severity: p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_BLOCKER, ListenerId: listenerID, RouteIds: routeIDs, Summary: summary, Detail: detail}
}

func migrationWarning(code string, listenerID int64, routeIDs []int64, summary, detail string) *p2pstreamv1.PublicSiteMigrationIssue {
	return &p2pstreamv1.PublicSiteMigrationIssue{Code: code, Severity: p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_WARNING, ListenerId: listenerID, RouteIds: routeIDs, Summary: summary, Detail: detail, RequiresAcknowledgement: true}
}

func migrationNotice(code string, listenerID int64, routeIDs []int64, summary, detail string) *p2pstreamv1.PublicSiteMigrationIssue {
	return &p2pstreamv1.PublicSiteMigrationIssue{Code: code, Severity: p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_WARNING, ListenerId: listenerID, RouteIds: routeIDs, Summary: summary, Detail: detail}
}

func routeIDs(routes []publicSiteMigrationRoute) []int64 {
	result := make([]int64, 0, len(routes))
	for _, route := range routes {
		result = append(result, route.ID)
	}
	return result
}

func publicSiteMigrationGroupKey(listenerID int64, host string) string {
	if host == "" {
		host = "default"
	}
	return strconv.FormatInt(listenerID, 10) + ":" + host
}

func publicSiteMigrationName(listenerID int64, host string) string {
	name := strings.ToLower(host)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name = strings.Trim(b.String(), ".-_")
	if name == "" {
		name = "default"
	}
	prefix := "migrated-" + strconv.FormatInt(listenerID, 10) + "-"
	if len(prefix)+len(name) > 64 {
		name = name[:64-len(prefix)]
	}
	return prefix + name
}

func addressIsHTTPSIncompatible(protocol, hostname string) bool {
	_, err := netip.ParseAddr(hostname)
	return protocol == publicListenerProtocolHTTPS && err == nil
}

func loadPublicSiteMigrationState(ctx context.Context, tx *sql.Tx, requested []int64) (publicSiteMigrationState, error) {
	listeners, err := loadPublicSiteMigrationListeners(ctx, tx)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	selected, err := selectPublicSiteMigrationListeners(listeners, requested)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	selectedSet := make(map[int64]struct{}, len(selected))
	for _, listener := range selected {
		selectedSet[listener.ID] = struct{}{}
	}
	routes, err := loadPublicSiteMigrationRoutes(ctx, tx, selectedSet)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	claims, err := loadPublicSiteMigrationClaims(ctx, tx, selectedSet)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	allCerts, err := db.New(tx).ListPublicTlsCertificates(ctx)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	certs := make([]db.PublicTlsCertificate, 0, len(allCerts))
	for _, cert := range allCerts {
		if _, ok := selectedSet[cert.ListenerID]; ok {
			certs = append(certs, cert)
		}
	}
	allTargets, err := db.New(tx).ListPublicRouteTargets(ctx)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	routeSet := make(map[int64]struct{}, len(routes))
	for _, route := range routes {
		routeSet[route.ID] = struct{}{}
	}
	targets := make([]db.PublicRouteTarget, 0, len(allTargets))
	for _, target := range allTargets {
		if _, ok := routeSet[target.RouteID]; ok {
			targets = append(targets, target)
		}
	}
	revision, err := publicSiteMigrationRevision(ctx, tx, selected, routes, claims)
	if err != nil {
		return publicSiteMigrationState{}, err
	}
	return publicSiteMigrationState{Listeners: selected, Routes: routes, Claims: claims, Certs: certs, Targets: targets, Revision: revision}, nil
}

func loadPublicSiteMigrationListeners(ctx context.Context, tx *sql.Tx) ([]publicSiteMigrationListener, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, protocol FROM public_listeners ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []publicSiteMigrationListener
	for rows.Next() {
		var listener publicSiteMigrationListener
		if err := rows.Scan(&listener.ID, &listener.Name, &listener.Protocol); err != nil {
			return nil, err
		}
		result = append(result, listener)
	}
	return result, rows.Err()
}

func selectPublicSiteMigrationListeners(all []publicSiteMigrationListener, requested []int64) ([]publicSiteMigrationListener, error) {
	byID := make(map[int64]publicSiteMigrationListener, len(all))
	for _, listener := range all {
		byID[listener.ID] = listener
	}
	if len(requested) == 0 {
		return all, nil
	}
	seen := make(map[int64]struct{}, len(requested))
	result := make([]publicSiteMigrationListener, 0, len(requested))
	for _, id := range requested {
		if id <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("listener IDs must be positive"))
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate listener ID %d", id))
		}
		seen[id] = struct{}{}
		listener, ok := byID[id]
		if !ok {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("public listener %d not found", id))
		}
		result = append(result, listener)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func loadPublicSiteMigrationRoutes(ctx context.Context, tx *sql.Tx, selected map[int64]struct{}) ([]publicSiteMigrationRoute, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, listener_id, priority, host_pattern, path_prefix, action, is_default, enabled FROM public_routes WHERE site_id IS NULL ORDER BY listener_id, priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []publicSiteMigrationRoute
	for rows.Next() {
		var route publicSiteMigrationRoute
		var listenerID sql.NullInt64
		var isDefault, enabled int64
		if err := rows.Scan(&route.ID, &listenerID, &route.Priority, &route.HostPattern, &route.PathPrefix, &route.Action, &isDefault, &enabled); err != nil {
			return nil, err
		}
		if !listenerID.Valid {
			return nil, fmt.Errorf("standalone route %d has no listener", route.ID)
		}
		if _, ok := selected[listenerID.Int64]; !ok {
			continue
		}
		route.ListenerID, route.IsDefault, route.Enabled = listenerID.Int64, isDefault != 0, enabled != 0
		result = append(result, route)
	}
	return result, rows.Err()
}

func loadPublicSiteMigrationClaims(ctx context.Context, tx *sql.Tx, selected map[int64]struct{}) ([]publicSiteMigrationClaim, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.id, b.listener_id, s.default_site, COALESCE(h.hostname_pattern, '')
		FROM public_sites s
		JOIN public_site_listener_bindings b ON b.site_id = s.id
		LEFT JOIN public_site_hosts h ON h.site_id = s.id
		WHERE s.published = 1
		ORDER BY b.listener_id, s.id, h.hostname_pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []publicSiteMigrationClaim
	for rows.Next() {
		var claim publicSiteMigrationClaim
		var defaultSite int64
		if err := rows.Scan(&claim.SiteID, &claim.ListenerID, &defaultSite, &claim.HostnamePattern); err != nil {
			return nil, err
		}
		if _, ok := selected[claim.ListenerID]; !ok {
			continue
		}
		claim.DefaultSite = defaultSite != 0
		result = append(result, claim)
	}
	return result, rows.Err()
}

func publicSiteMigrationRevision(ctx context.Context, tx *sql.Tx, listeners []publicSiteMigrationListener, routes []publicSiteMigrationRoute, claims []publicSiteMigrationClaim) (string, error) {
	digest := sha256.New()
	writeMigrationHashValue(digest, "public-site-migration-v1")
	for _, listener := range listeners {
		writeMigrationHashValue(digest, listener.ID, listener.Name, listener.Protocol)
	}
	for _, route := range routes {
		writeMigrationHashValue(digest, route.ID, route.ListenerID, route.Priority, route.HostPattern, route.PathPrefix, route.Action, route.IsDefault, route.Enabled)
	}
	for _, claim := range claims {
		writeMigrationHashValue(digest, claim.SiteID, claim.ListenerID, claim.DefaultSite, claim.HostnamePattern)
	}
	ids := make([]int64, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	if len(ids) > 0 {
		placeholders, args := publicSiteMigrationSQLIDs(ids)
		queries := []string{
			`SELECT * FROM public_routes WHERE id IN (` + placeholders + `) ORDER BY id`,
			`SELECT t.* FROM public_route_targets t WHERE t.route_id IN (` + placeholders + `) ORDER BY t.route_id, t.id`,
			`SELECT h.* FROM public_route_target_upstream_headers h JOIN public_route_targets t ON t.id = h.target_id WHERE t.route_id IN (` + placeholders + `) ORDER BY h.target_id, h.id`,
			`SELECT h.* FROM public_route_target_response_headers h JOIN public_route_targets t ON t.id = h.target_id WHERE t.route_id IN (` + placeholders + `) ORDER BY h.target_id, h.id`,
			`SELECT p.* FROM public_access_policies p WHERE p.id IN (SELECT access_policy_id FROM public_routes WHERE id IN (` + placeholders + `)) ORDER BY p.id`,
			`SELECT a.* FROM public_access_providers a WHERE a.id IN (SELECT provider_id FROM public_access_policies WHERE id IN (SELECT access_policy_id FROM public_routes WHERE id IN (` + placeholders + `))) ORDER BY a.id`,
			`SELECT r.* FROM public_response_templates r WHERE r.id IN (SELECT static_response_template_id FROM public_route_targets WHERE route_id IN (` + placeholders + `)) ORDER BY r.id`,
		}
		for _, query := range queries {
			if err := hashPublicSiteMigrationQuery(ctx, tx, digest, query, args...); err != nil {
				return "", err
			}
		}
	}
	listenerIDs := make([]int64, 0, len(listeners))
	for _, listener := range listeners {
		listenerIDs = append(listenerIDs, listener.ID)
	}
	if len(listenerIDs) > 0 {
		placeholders, args := publicSiteMigrationSQLIDs(listenerIDs)
		for _, query := range []string{
			`SELECT * FROM public_listeners WHERE id IN (` + placeholders + `) ORDER BY id`,
			`SELECT * FROM public_site_migrated_listeners WHERE listener_id IN (` + placeholders + `) ORDER BY listener_id`,
			`SELECT s.*, b.listener_id, b.behavior, b.redirect_listener_id, b.redirect_hostname FROM public_sites s JOIN public_site_listener_bindings b ON b.site_id = s.id WHERE b.listener_id IN (` + placeholders + `) ORDER BY b.listener_id, s.id`,
			`SELECT h.* FROM public_site_hosts h JOIN public_site_listener_bindings b ON b.site_id = h.site_id WHERE b.listener_id IN (` + placeholders + `) ORDER BY b.listener_id, h.site_id, h.id`,
			`SELECT * FROM public_tls_certificates WHERE listener_id IN (` + placeholders + `) ORDER BY listener_id, hostname_pattern, id`,
		} {
			if err := hashPublicSiteMigrationQuery(ctx, tx, digest, query, args...); err != nil {
				return "", err
			}
		}
	}
	for _, query := range []string{
		`SELECT id, route_ids_json, target_ids_json, updated_at FROM public_cache_rules ORDER BY id`,
		`SELECT id, route_ids_json, target_ids_json, updated_at FROM public_retry_rules ORDER BY id`,
	} {
		if err := hashPublicSiteMigrationQuery(ctx, tx, digest, query); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashPublicSiteMigrationQuery(ctx context.Context, tx *sql.Tx, digest hash.Hash, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for rows.Next() {
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		writeMigrationHashValue(digest, columns)
		for _, value := range values {
			if bytes, ok := value.([]byte); ok {
				value = sha256.Sum256(bytes)
			}
			writeMigrationHashValue(digest, value)
		}
	}
	return rows.Err()
}

func writeMigrationHashValue(digest hash.Hash, values ...any) {
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		_, _ = digest.Write([]byte(strconv.Itoa(len(encoded))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write(encoded)
		_, _ = digest.Write([]byte{'\n'})
	}
}

func publicSiteMigrationSQLIDs(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	return strings.Join(placeholders, ","), args
}

func publicSiteMigrationError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return err
	}
	return publicDBError(err)
}

func applyPublicSiteMigrationPlan(ctx context.Context, tx *sql.Tx, plan publicSiteMigrationPlan) ([]*p2pstreamv1.PublicSite, []*p2pstreamv1.PublicSiteMigrationRouteMapping, error) {
	var created []*p2pstreamv1.PublicSite
	var mappings []*p2pstreamv1.PublicSiteMigrationRouteMapping
	routeCopies := make(map[int64][]int64)
	targetCopies := make(map[int64][]int64)
	for _, group := range plan.Groups {
		defaultSite := group.HostnameMode == p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_DEFAULT
		canonical := ""
		if !defaultSite && len(group.HostnamePatterns) > 0 {
			canonical = group.HostnamePatterns[0]
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO public_sites (name, enabled, published, default_site, canonical_hostname) VALUES (?, 1, 1, ?, ?)`, group.ProposedSiteName, boolInt(defaultSite), canonical)
		if err != nil {
			return nil, nil, err
		}
		siteID, err := result.LastInsertId()
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public_site_listener_bindings (site_id, listener_id, behavior) VALUES (?, ?, 'serve')`, siteID, group.ListenerId); err != nil {
			return nil, nil, err
		}
		siteProto := &p2pstreamv1.PublicSite{Id: siteID, ListenerId: group.ListenerId, Name: group.ProposedSiteName, Enabled: true, Published: true, DefaultSite: defaultSite, CanonicalHostname: canonical, ListenerBindings: []*p2pstreamv1.PublicSiteListenerBinding{{ListenerId: group.ListenerId, Behavior: p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE}}}
		for i, hostname := range group.HostnamePatterns {
			role := publicSiteHostRoleAlias
			if i == 0 && hostname == canonical {
				role = publicSiteHostRolePrimary
			}
			hostResult, err := tx.ExecContext(ctx, `INSERT INTO public_site_hosts (site_id, hostname_pattern, role, behavior) VALUES (?, ?, ?, 'serve')`, siteID, hostname, role)
			if err != nil {
				return nil, nil, err
			}
			hostID, err := hostResult.LastInsertId()
			if err != nil {
				return nil, nil, err
			}
			siteProto.Hosts = append(siteProto.Hosts, &p2pstreamv1.PublicSiteHost{Id: hostID, SiteId: siteID, HostnamePattern: hostname, Primary: role == publicSiteHostRolePrimary, Behavior: p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE})
		}
		for _, routeID := range group.SourceRouteIds {
			route, ok := plan.Routes[routeID]
			if !ok {
				return nil, nil, fmt.Errorf("migration route %d disappeared from plan", routeID)
			}
			result, err := tx.ExecContext(ctx, `UPDATE public_routes SET listener_id = 0, site_id = ?, host_pattern = '', updated_at = CURRENT_TIMESTAMP WHERE id = ? AND listener_id = ? AND site_id IS NULL`, siteID, routeID, route.ListenerID)
			if err != nil {
				return nil, nil, err
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return nil, nil, fmt.Errorf("standalone route %d changed during migration", routeID)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO public_site_migration_route_mappings (source_route_id, destination_site_id, destination_route_id, copied) VALUES (?, ?, ?, 0)`, routeID, siteID, routeID); err != nil {
				return nil, nil, err
			}
			mappings = append(mappings, &p2pstreamv1.PublicSiteMigrationRouteMapping{SourceRouteId: routeID, DestinationSiteId: siteID, DestinationRouteId: routeID})
			if err := recordIdentityTargetMappings(ctx, tx, routeID); err != nil {
				return nil, nil, err
			}
		}
		for _, copySpec := range group.RouteCopies {
			newRouteID, copiedTargets, err := copyPublicSiteMigrationRoute(ctx, tx, copySpec.SourceRouteId, siteID)
			if err != nil {
				return nil, nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO public_site_migration_route_mappings (source_route_id, destination_site_id, destination_route_id, copied) VALUES (?, ?, ?, 1)`, copySpec.SourceRouteId, siteID, newRouteID); err != nil {
				return nil, nil, err
			}
			routeCopies[copySpec.SourceRouteId] = append(routeCopies[copySpec.SourceRouteId], newRouteID)
			for source, destination := range copiedTargets {
				targetCopies[source] = append(targetCopies[source], destination)
			}
			mappings = append(mappings, &p2pstreamv1.PublicSiteMigrationRouteMapping{SourceRouteId: copySpec.SourceRouteId, DestinationSiteId: siteID, DestinationRouteId: newRouteID, Copied: true})
		}
		created = append(created, siteProto)
	}
	if err := expandPublicSiteMigrationPolicyScopes(ctx, tx, routeCopies, targetCopies); err != nil {
		return nil, nil, err
	}
	return created, mappings, nil
}

func copyPublicSiteMigrationRoute(ctx context.Context, tx *sql.Tx, sourceRouteID, siteID int64) (int64, map[int64]int64, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO public_routes (
			listener_id, site_id, priority, host_pattern, path_prefix, target_load_balancing,
			is_default, action, redirect_target_mode, redirect_target, redirect_status_code,
			redirect_preserve_path_suffix, redirect_preserve_query, path_security_mode,
			access_policy_id, enabled
		)
		SELECT 0, ?, priority, '', path_prefix, target_load_balancing,
			is_default, action, redirect_target_mode, redirect_target, redirect_status_code,
			redirect_preserve_path_suffix, redirect_preserve_query, path_security_mode,
			access_policy_id, enabled
		FROM public_routes WHERE id = ? AND site_id IS NULL
		RETURNING id`, siteID, sourceRouteID)
	var destinationRouteID int64
	if err := row.Scan(&destinationRouteID); err != nil {
		return 0, nil, err
	}
	targetRows, err := tx.QueryContext(ctx, `SELECT id FROM public_route_targets WHERE route_id = ? ORDER BY position, id`, sourceRouteID)
	if err != nil {
		return 0, nil, err
	}
	var sourceTargetIDs []int64
	for targetRows.Next() {
		var id int64
		if err := targetRows.Scan(&id); err != nil {
			targetRows.Close()
			return 0, nil, err
		}
		sourceTargetIDs = append(sourceTargetIDs, id)
	}
	if err := targetRows.Close(); err != nil {
		return 0, nil, err
	}
	targetMappings := make(map[int64]int64, len(sourceTargetIDs))
	for _, sourceTargetID := range sourceTargetIDs {
		var destinationTargetID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO public_route_targets (
				route_id, name, position, priority_group, weight, enabled, target_type, url,
				transport, agent_selector_json, agent_load_balancing, tls_skip_verify,
				upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password,
				upstream_response_header_timeout_millis, health_check_enabled, health_check_method,
				health_check_path, health_check_interval_millis, health_check_timeout_millis,
				health_check_healthy_threshold, health_check_unhealthy_threshold,
				health_check_expected_status_min, health_check_expected_status_max,
				static_status_code, static_response_body, static_response_body_mode,
				static_response_template_id
			)
			SELECT ?, name, position, priority_group, weight, enabled, target_type, url,
				transport, agent_selector_json, agent_load_balancing, tls_skip_verify,
				upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password,
				upstream_response_header_timeout_millis, health_check_enabled, health_check_method,
				health_check_path, health_check_interval_millis, health_check_timeout_millis,
				health_check_healthy_threshold, health_check_unhealthy_threshold,
				health_check_expected_status_min, health_check_expected_status_max,
				static_status_code, static_response_body, static_response_body_mode,
				static_response_template_id
			FROM public_route_targets WHERE id = ?
			RETURNING id`, destinationRouteID, sourceTargetID).Scan(&destinationTargetID)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public_route_target_upstream_headers (target_id, position, name, value, sensitive) SELECT ?, position, name, value, sensitive FROM public_route_target_upstream_headers WHERE target_id = ? ORDER BY position, id`, destinationTargetID, sourceTargetID); err != nil {
			return 0, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public_route_target_response_headers (target_id, position, name, value) SELECT ?, position, name, value FROM public_route_target_response_headers WHERE target_id = ? ORDER BY position, id`, destinationTargetID, sourceTargetID); err != nil {
			return 0, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public_site_migration_target_mappings (source_target_id, destination_route_id, destination_target_id) VALUES (?, ?, ?)`, sourceTargetID, destinationRouteID, destinationTargetID); err != nil {
			return 0, nil, err
		}
		targetMappings[sourceTargetID] = destinationTargetID
	}
	return destinationRouteID, targetMappings, nil
}

func recordIdentityTargetMappings(ctx context.Context, tx *sql.Tx, routeID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO public_site_migration_target_mappings (source_target_id, destination_route_id, destination_target_id)
		SELECT id, route_id, id FROM public_route_targets WHERE route_id = ?`, routeID)
	return err
}

func expandPublicSiteMigrationPolicyScopes(ctx context.Context, tx *sql.Tx, routeCopies, targetCopies map[int64][]int64) error {
	for _, table := range []string{"public_cache_rules", "public_retry_rules"} {
		rows, err := tx.QueryContext(ctx, `SELECT id, route_ids_json, target_ids_json FROM `+table+` ORDER BY id`)
		if err != nil {
			return err
		}
		type update struct {
			id              int64
			routes, targets string
		}
		var updates []update
		for rows.Next() {
			var id int64
			var routeJSON, targetJSON string
			if err := rows.Scan(&id, &routeJSON, &targetJSON); err != nil {
				rows.Close()
				return err
			}
			routeJSON, routeChanged, err := expandPublicSiteMigrationIDJSON(routeJSON, routeCopies)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%s rule %d route scope: %w", table, id, err)
			}
			targetJSON, targetChanged, err := expandPublicSiteMigrationIDJSON(targetJSON, targetCopies)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%s rule %d target scope: %w", table, id, err)
			}
			if routeChanged || targetChanged {
				updates = append(updates, update{id: id, routes: routeJSON, targets: targetJSON})
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET route_ids_json = ?, target_ids_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, item.routes, item.targets, item.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandPublicSiteMigrationIDJSON(raw string, copies map[int64][]int64) (string, bool, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return "", false, err
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	changed := false
	for _, source := range append([]int64(nil), ids...) {
		for _, destination := range copies[source] {
			if _, ok := seen[destination]; ok {
				continue
			}
			seen[destination] = struct{}{}
			ids = append(ids, destination)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := json.Marshal(ids)
	return string(encoded), true, err
}
