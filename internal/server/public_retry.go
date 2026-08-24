package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	publicRetryFailureModeConnectionFailures  = "connection_failures"
	publicRetryFailureModePreResponseFailures = "pre_response_failures"
	publicRetryBodyModeNever                  = "never"
	publicRetryBodyModeBuffered               = "buffered"
	publicRetryOutcomeRecovered               = "recovered"
	publicRetryOutcomeExhausted               = "exhausted"
	publicRetryOutcomeSkipped                 = "skipped"

	defaultPublicRetryMaxRetries        = int64(1)
	maxPublicRetryMaxRetries            = int64(3)
	maxPublicRetryMethods               = 32
	maxPublicRetryFilters               = 64
	maxPublicRetryReplayBodyBytes       = int64(4 << 20)
	defaultPublicRetryReplayBudgetBytes = int64(64 << 20)
)

var defaultPublicRetryMethods = []string{http.MethodGet, http.MethodHead}

type publicRetryRuleConfig struct {
	ID                 int64
	Name               string
	Priority           int64
	Enabled            bool
	Methods            []string
	methodSet          map[string]struct{}
	AllMethods         bool
	MaxRetries         int64
	FailureMode        string
	BodyMode           string
	MaxReplayBodyBytes int64
	RouteIDs           []int64
	routeIDSet         map[int64]struct{}
	TargetIDs          []int64
	targetIDSet        map[int64]struct{}
	Match              publicPolicyMatchConfig
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type publicRetryRuleMutationInput struct {
	Name               string
	Priority           int64
	Enabled            int64
	MethodsJSON        string
	MaxRetries         int64
	FailureMode        string
	BodyMode           string
	MaxReplayBodyBytes int64
	RouteIDsJSON       string
	TargetIDsJSON      string
	MatchJSON          string
}

func (rule publicRetryRuleConfig) matches(listener publicListenerConfig, r *http.Request, resolution publicRouteResolution) bool {
	if !rule.Enabled || r == nil || resolution.Target.Transport != publicRouteTargetTransportAgent {
		return false
	}
	if !rule.AllMethods {
		if _, ok := rule.methodSet[strings.ToUpper(r.Method)]; !ok {
			return false
		}
	}
	if len(rule.routeIDSet) > 0 {
		if _, ok := rule.routeIDSet[resolution.Route.ID]; !ok {
			return false
		}
	}
	if len(rule.targetIDSet) > 0 {
		if _, ok := rule.targetIDSet[resolution.Target.ID]; !ok {
			return false
		}
	}
	return rule.Match.matches(listener, r)
}

func selectPublicRetryRule(snap *publicProxySnapshot, r *http.Request, resolution publicRouteResolution) *publicRetryRuleConfig {
	if snap == nil || r == nil || publicRetryRequestAlwaysExcluded(r) {
		return nil
	}
	for i := range snap.RetryRules {
		if snap.RetryRules[i].matches(resolution.Listener, r, resolution) {
			rule := snap.RetryRules[i]
			return &rule
		}
	}
	return nil
}

func publicRetryRequestAlwaysExcluded(r *http.Request) bool {
	if r == nil {
		return true
	}
	if strings.EqualFold(r.Method, http.MethodConnect) || strings.EqualFold(r.Method, http.MethodTrace) {
		return true
	}
	return requestWantsProtocolUpgrade(r)
}

func requestWantsProtocolUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Upgrade")) != "" {
		return true
	}
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

func normalizePublicRetryMethods(values []string) ([]string, error) {
	if len(values) == 0 {
		return append([]string(nil), defaultPublicRetryMethods...), nil
	}
	if len(values) > maxPublicRetryMethods {
		return nil, errors.New("retry rule has too many request methods")
	}
	seen := make(map[string]struct{}, len(values))
	methods := make([]string, 0, len(values))
	for _, raw := range values {
		method := strings.ToUpper(strings.TrimSpace(raw))
		if method == "" || !validHTTPToken(method) {
			return nil, fmt.Errorf("retry method %q is invalid", raw)
		}
		if method == http.MethodConnect || method == http.MethodTrace {
			return nil, fmt.Errorf("retry method %s is not supported", method)
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		methods = append(methods, method)
	}
	if _, all := seen["*"]; all && len(methods) != 1 {
		return nil, errors.New("retry method * must be used by itself")
	}
	sort.Strings(methods)
	return methods, nil
}

func publicRetryRequiresDuplicateRiskAcknowledgement(methods []string, failureMode string) bool {
	if failureMode == publicRetryFailureModePreResponseFailures {
		return true
	}
	for _, method := range methods {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			return true
		}
	}
	return false
}

func normalizePublicRetryFailureMode(value string) string {
	switch value {
	case publicRetryFailureModePreResponseFailures:
		return publicRetryFailureModePreResponseFailures
	default:
		return publicRetryFailureModeConnectionFailures
	}
}

func normalizePublicRetryBodyMode(value string) string {
	if value == publicRetryBodyModeBuffered {
		return publicRetryBodyModeBuffered
	}
	return publicRetryBodyModeNever
}

func protoPublicRetryFailureMode(value string) p2pstreamv1.PublicRetryFailureMode {
	if normalizePublicRetryFailureMode(value) == publicRetryFailureModePreResponseFailures {
		return p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES
	}
	return p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES
}

func publicRetryFailureModeFromProto(value p2pstreamv1.PublicRetryFailureMode) string {
	if value == p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES {
		return publicRetryFailureModePreResponseFailures
	}
	return publicRetryFailureModeConnectionFailures
}

func protoPublicRetryBodyMode(value string) p2pstreamv1.PublicRetryBodyMode {
	if normalizePublicRetryBodyMode(value) == publicRetryBodyModeBuffered {
		return p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED
	}
	return p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER
}

func publicRetryBodyModeFromProto(value p2pstreamv1.PublicRetryBodyMode) string {
	if value == p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED {
		return publicRetryBodyModeBuffered
	}
	return publicRetryBodyModeNever
}

func publicRetryRuleRowToConfig(row db.PublicRetryRule) (publicRetryRuleConfig, error) {
	var methods []string
	if err := json.Unmarshal([]byte(row.MethodsJson), &methods); err != nil {
		return publicRetryRuleConfig{}, err
	}
	methods, err := normalizePublicRetryMethods(methods)
	if err != nil {
		return publicRetryRuleConfig{}, err
	}
	routeIDs, err := publicCacheInt64ListFromJSON(row.RouteIdsJson)
	if err != nil {
		return publicRetryRuleConfig{}, err
	}
	targetIDs, err := publicCacheInt64ListFromJSON(row.TargetIdsJson)
	if err != nil {
		return publicRetryRuleConfig{}, err
	}
	match, err := decodePublicPolicyMatchJSON(row.MatchJson)
	if err != nil {
		return publicRetryRuleConfig{}, err
	}
	maxRetries := row.MaxRetries
	if maxRetries < 1 {
		maxRetries = defaultPublicRetryMaxRetries
	}
	if maxRetries > maxPublicRetryMaxRetries {
		maxRetries = maxPublicRetryMaxRetries
	}
	bodyMode := normalizePublicRetryBodyMode(row.BodyMode)
	maxReplayBodyBytes := row.MaxReplayBodyBytes
	if bodyMode == publicRetryBodyModeNever {
		maxReplayBodyBytes = 0
	}
	methodSet := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		methodSet[method] = struct{}{}
	}
	routeIDSet := make(map[int64]struct{}, len(routeIDs))
	for _, id := range routeIDs {
		routeIDSet[id] = struct{}{}
	}
	targetIDSet := make(map[int64]struct{}, len(targetIDs))
	for _, id := range targetIDs {
		targetIDSet[id] = struct{}{}
	}
	return publicRetryRuleConfig{
		ID: row.ID, Name: row.Name, Priority: row.Priority, Enabled: row.Enabled != 0,
		Methods: methods, methodSet: methodSet, AllMethods: len(methods) == 1 && methods[0] == "*",
		MaxRetries: maxRetries, FailureMode: normalizePublicRetryFailureMode(row.FailureMode),
		BodyMode: bodyMode, MaxReplayBodyBytes: maxReplayBodyBytes,
		RouteIDs: routeIDs, routeIDSet: routeIDSet, TargetIDs: targetIDs, targetIDSet: targetIDSet,
		Match: match, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func publicRetryRuleConfigToProto(rule publicRetryRuleConfig) *p2pstreamv1.PublicRetryRule {
	return &p2pstreamv1.PublicRetryRule{
		Id: rule.ID, Name: rule.Name, Priority: rule.Priority, Enabled: rule.Enabled,
		Methods: append([]string(nil), rule.Methods...), MaxRetries: rule.MaxRetries,
		FailureMode: protoPublicRetryFailureMode(rule.FailureMode), BodyMode: protoPublicRetryBodyMode(rule.BodyMode),
		MaxReplayBodyBytes: rule.MaxReplayBodyBytes, RouteIds: append([]int64(nil), rule.RouteIDs...),
		TargetIds: append([]int64(nil), rule.TargetIDs...), MatchRule: publicPolicyMatchRuleToProto(rule.Match),
		CreatedAtUnixMillis: rule.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: rule.UpdatedAt.UnixMilli(),
	}
}

func publicRetryRulesToProto(rows []db.PublicRetryRule) []*p2pstreamv1.PublicRetryRule {
	result := make([]*p2pstreamv1.PublicRetryRule, 0, len(rows))
	for _, row := range rows {
		rule, err := publicRetryRuleRowToConfig(row)
		if err == nil {
			result = append(result, publicRetryRuleConfigToProto(rule))
		}
	}
	return result
}

func sortPublicRetryRules(rules []publicRetryRuleConfig) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority == rules[j].Priority {
			return rules[i].ID < rules[j].ID
		}
		return rules[i].Priority < rules[j].Priority
	})
}

func (a *App) validatePublicRetryRuleInput(
	ctx context.Context,
	name string,
	priority int64,
	enabled bool,
	methods []string,
	maxRetries int64,
	failureMode p2pstreamv1.PublicRetryFailureMode,
	bodyMode p2pstreamv1.PublicRetryBodyMode,
	maxReplayBodyBytes int64,
	routeIDs []int64,
	targetIDs []int64,
	matchRule *p2pstreamv1.PublicPolicyMatchRule,
	duplicateRiskAcknowledged bool,
) (publicRetryRuleMutationInput, error) {
	name = strings.TrimSpace(name)
	if !publicNamePattern.MatchString(name) {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("retry rule name must be 1-64 letters, numbers, dots, underscores, or hyphens"))
	}
	methods, err := normalizePublicRetryMethods(methods)
	if err != nil {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if maxRetries < 1 || maxRetries > maxPublicRetryMaxRetries {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("maximum retries must be between 1 and %d", maxPublicRetryMaxRetries))
	}
	failureModeString := publicRetryFailureModeFromProto(failureMode)
	if publicRetryRequiresDuplicateRiskAcknowledgement(methods, failureModeString) && !duplicateRiskAcknowledged {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate request risk must be acknowledged for pre-response failures or methods with side effects"))
	}
	bodyModeString := publicRetryBodyModeFromProto(bodyMode)
	if bodyModeString == publicRetryBodyModeNever {
		maxReplayBodyBytes = 0
	} else if maxReplayBodyBytes < 1 || maxReplayBodyBytes > maxPublicRetryReplayBodyBytes {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("maximum replay body must be between 1 byte and %d bytes", maxPublicRetryReplayBodyBytes))
	}
	routeIDs = normalizePublicCacheInt64List(routeIDs)
	targetIDs = normalizePublicCacheInt64List(targetIDs)
	if len(routeIDs) > maxPublicRetryFilters || len(targetIDs) > maxPublicRetryFilters {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("retry rules can filter at most 64 routes and 64 targets"))
	}
	for _, id := range routeIDs {
		if _, err := a.DB.GetPublicRoute(ctx, id); err != nil {
			return publicRetryRuleMutationInput{}, publicDBError(err)
		}
	}
	for _, id := range targetIDs {
		target, err := a.DB.GetPublicRouteTarget(ctx, id)
		if err != nil {
			return publicRetryRuleMutationInput{}, publicDBError(err)
		}
		if normalizePublicRouteTargetType(target.TargetType) != publicRouteTargetTypeProxy || normalizePublicRouteTargetTransport(target.Transport) != publicRouteTargetTransportAgent {
			return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %d is not an agent proxy target", id))
		}
	}
	match, err := validatePublicPolicyMatch(matchRule)
	if err != nil {
		return publicRetryRuleMutationInput{}, err
	}
	methodsJSON, _ := json.Marshal(methods)
	routeIDsJSON, _ := json.Marshal(routeIDs)
	targetIDsJSON, _ := json.Marshal(targetIDs)
	matchJSON, err := json.Marshal(match)
	if err != nil {
		return publicRetryRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	return publicRetryRuleMutationInput{
		Name: name, Priority: priority, Enabled: boolInt(enabled), MethodsJSON: string(methodsJSON),
		MaxRetries: maxRetries, FailureMode: failureModeString, BodyMode: bodyModeString,
		MaxReplayBodyBytes: maxReplayBodyBytes, RouteIDsJSON: string(routeIDsJSON),
		TargetIDsJSON: string(targetIDsJSON), MatchJSON: string(matchJSON),
	}, nil
}

func (a *App) CreatePublicRetryRule(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicRetryRuleRequest]) (*connect.Response[p2pstreamv1.CreatePublicRetryRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	input, err := a.validatePublicRetryRuleInput(ctx, req.Msg.Name, req.Msg.Priority, req.Msg.Enabled, req.Msg.Methods, req.Msg.MaxRetries, req.Msg.FailureMode, req.Msg.BodyMode, req.Msg.MaxReplayBodyBytes, req.Msg.RouteIds, req.Msg.TargetIds, req.Msg.MatchRule, req.Msg.DuplicateRiskAcknowledged)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicRetryRule(ctx, db.CreatePublicRetryRuleParams{
		Name: input.Name, Priority: input.Priority, Enabled: input.Enabled, MethodsJson: input.MethodsJSON,
		MaxRetries: input.MaxRetries, FailureMode: input.FailureMode, BodyMode: input.BodyMode,
		MaxReplayBodyBytes: input.MaxReplayBodyBytes, RouteIdsJson: input.RouteIDsJSON,
		TargetIdsJson: input.TargetIDsJSON, MatchJson: input.MatchJSON,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicRetryRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicRetryRuleResponse{Rule: publicRetryRuleConfigToProto(rule)}), nil
}

func (a *App) UpdatePublicRetryRule(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicRetryRuleRequest]) (*connect.Response[p2pstreamv1.UpdatePublicRetryRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	input, err := a.validatePublicRetryRuleInput(ctx, req.Msg.Name, req.Msg.Priority, req.Msg.Enabled, req.Msg.Methods, req.Msg.MaxRetries, req.Msg.FailureMode, req.Msg.BodyMode, req.Msg.MaxReplayBodyBytes, req.Msg.RouteIds, req.Msg.TargetIds, req.Msg.MatchRule, req.Msg.DuplicateRiskAcknowledged)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicRetryRule(ctx, db.UpdatePublicRetryRuleParams{
		ID: req.Msg.Id, Name: input.Name, Priority: input.Priority, Enabled: input.Enabled,
		MethodsJson: input.MethodsJSON, MaxRetries: input.MaxRetries, FailureMode: input.FailureMode,
		BodyMode: input.BodyMode, MaxReplayBodyBytes: input.MaxReplayBodyBytes,
		RouteIdsJson: input.RouteIDsJSON, TargetIdsJson: input.TargetIDsJSON, MatchJson: input.MatchJSON,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicRetryRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRetryRuleResponse{Rule: publicRetryRuleConfigToProto(rule)}), nil
}

func (a *App) DeletePublicRetryRule(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicRetryRuleRequest]) (*connect.Response[p2pstreamv1.DeletePublicRetryRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicRetryRule(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicRetryRuleResponse{}), nil
}
