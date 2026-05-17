package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	maxPublicPolicyMatchExpressionBytes = 4096
	maxPublicPolicyMatchBuilderNodes    = 64
	maxPublicPolicyMatchConditionValues = 64
	maxPublicPolicyMatchValueBytes      = 512
	publicPolicyMatchCostLimit          = 20000

	publicPolicyMatchBooleanAll = "all"
	publicPolicyMatchBooleanAny = "any"

	publicPolicyMatchFieldMethod     = "method"
	publicPolicyMatchFieldProtocol   = "protocol"
	publicPolicyMatchFieldHost       = "host"
	publicPolicyMatchFieldPath       = "path"
	publicPolicyMatchFieldRemoteIP   = "remote_ip"
	publicPolicyMatchFieldHeader     = "header"
	publicPolicyMatchFieldCookie     = "cookie"
	publicPolicyMatchFieldQueryParam = "query_param"

	publicPolicyMatchOperatorPresent     = "present"
	publicPolicyMatchOperatorEquals      = "equals"
	publicPolicyMatchOperatorPrefix      = "prefix"
	publicPolicyMatchOperatorSuffix      = "suffix"
	publicPolicyMatchOperatorContains    = "contains"
	publicPolicyMatchOperatorMatches     = "matches"
	publicPolicyMatchOperatorIn          = "in"
	publicPolicyMatchOperatorCIDR        = "cidr"
	publicPolicyMatchOperatorHostPattern = "host_pattern"
)

type publicPolicyMatchBuilderConfig struct {
	Root *publicPolicyMatchGroupConfig `json:"root,omitempty"`
}

type publicPolicyMatchGroupConfig struct {
	Operator   string                             `json:"operator,omitempty"`
	Conditions []publicPolicyMatchConditionConfig `json:"conditions,omitempty"`
	Groups     []publicPolicyMatchGroupConfig     `json:"groups,omitempty"`
	Negated    bool                               `json:"negated,omitempty"`
}

type publicPolicyMatchConditionConfig struct {
	Field    string   `json:"field"`
	Name     string   `json:"name,omitempty"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
	Negated  bool     `json:"negated,omitempty"`
}

var (
	publicPolicyMatchEnvOnce sync.Once
	publicPolicyMatchEnv     *cel.Env
	publicPolicyMatchEnvErr  error
)

func publicPolicyMatchCELEnv() (*cel.Env, error) {
	publicPolicyMatchEnvOnce.Do(func() {
		publicPolicyMatchEnv, publicPolicyMatchEnvErr = cel.NewEnv(
			cel.Variable("method", cel.StringType),
			cel.Variable("protocol", cel.StringType),
			cel.Variable("host", cel.StringType),
			cel.Variable("path", cel.StringType),
			cel.Variable("remote_ip", cel.StringType),
			cel.Variable("headers", cel.MapType(cel.StringType, cel.ListType(cel.StringType))),
			cel.Variable("cookies", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("query", cel.MapType(cel.StringType, cel.ListType(cel.StringType))),
			cel.Function("host_match",
				cel.Overload("host_match_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
					cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
						host, ok := publicPolicyMatchCELString(lhs)
						if !ok {
							return types.False
						}
						pattern, ok := publicPolicyMatchCELString(rhs)
						if !ok {
							return types.False
						}
						return types.Bool(hostMatchesPattern(host, normalizeHostPattern(pattern)))
					}),
				),
			),
			cel.Function("path_prefix",
				cel.Overload("path_prefix_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
					cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
						path, ok := publicPolicyMatchCELString(lhs)
						if !ok {
							return types.False
						}
						prefix, ok := publicPolicyMatchCELString(rhs)
						if !ok {
							return types.False
						}
						return types.Bool(pathPrefixMatches(path, prefix))
					}),
				),
			),
			cel.Function("cidr",
				cel.Overload("cidr_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
					cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
						ip, ok := publicPolicyMatchCELString(lhs)
						if !ok {
							return types.False
						}
						cidr, ok := publicPolicyMatchCELString(rhs)
						if !ok {
							return types.False
						}
						return types.Bool(publicPolicyMatchCIDR(ip, cidr))
					}),
				),
			),
		)
	})
	return publicPolicyMatchEnv, publicPolicyMatchEnvErr
}

func publicPolicyMatchCELString(value ref.Val) (string, bool) {
	raw, ok := value.Value().(string)
	return raw, ok
}

func publicPolicyMatchCIDR(rawIP string, rawCIDR string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return false
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(rawCIDR))
	if err != nil {
		return false
	}
	return prefix.Contains(addr)
}

func decodePublicPolicyMatchJSON(raw string) (publicPolicyMatchConfig, error) {
	var match publicPolicyMatchConfig
	if strings.TrimSpace(raw) == "" {
		return match, nil
	}
	if err := json.Unmarshal([]byte(raw), &match); err != nil {
		return publicPolicyMatchConfig{}, err
	}
	if err := compilePublicPolicyMatch(&match); err != nil {
		return publicPolicyMatchConfig{}, err
	}
	return match, nil
}

func validatePublicPolicyMatch(matchRule *p2pstreamv1.PublicPolicyMatchRule) (publicPolicyMatchConfig, error) {
	config, err := publicPolicyMatchConfigFromProto(matchRule)
	if err != nil {
		return publicPolicyMatchConfig{}, err
	}
	if err := compilePublicPolicyMatch(&config); err != nil {
		return publicPolicyMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return config, nil
}

func compilePublicPolicyMatch(match *publicPolicyMatchConfig) error {
	if match == nil {
		return nil
	}
	if strings.TrimSpace(match.CELExpression) == "" && match.Builder != nil {
		expr, err := publicPolicyMatchBuilderExpression(match.Builder)
		if err != nil {
			return err
		}
		match.CELExpression = expr
	}
	match.CELExpression = strings.TrimSpace(match.CELExpression)
	if match.CELExpression == "" {
		match.program = nil
		return nil
	}
	if len(match.CELExpression) > maxPublicPolicyMatchExpressionBytes {
		return fmt.Errorf("policy match expression is too large")
	}
	if match.Builder != nil {
		if _, err := publicPolicyMatchBuilderExpression(match.Builder); err != nil {
			return err
		}
	}
	env, err := publicPolicyMatchCELEnv()
	if err != nil {
		return err
	}
	ast, issues := env.Compile(match.CELExpression)
	if issues.Err() != nil {
		return fmt.Errorf("policy match expression is invalid: %w", issues.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return fmt.Errorf("policy match expression must evaluate to bool")
	}
	if err := validatePublicPolicyMatchCELAst(ast); err != nil {
		return err
	}
	program, err := env.Program(ast,
		cel.EvalOptions(cel.OptOptimize),
		cel.OptimizeRegex(interpreter.MatchesRegexOptimization),
		cel.InterruptCheckFrequency(100),
		cel.CostLimit(publicPolicyMatchCostLimit),
	)
	if err != nil {
		return fmt.Errorf("policy match expression program is invalid: %w", err)
	}
	match.program = program
	return nil
}

func validatePublicPolicyMatchCELAst(checked *cel.Ast) error {
	if checked == nil || checked.NativeRep() == nil || checked.NativeRep().Expr() == nil {
		return nil
	}
	var validationErr error
	visitor := celast.NewExprVisitor(func(expr celast.Expr) {
		if validationErr != nil || expr.Kind() != celast.CallKind {
			return
		}
		call := expr.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case "cidr":
			if len(args) == 2 {
				if value, ok := publicPolicyMatchStringLiteral(args[1]); ok {
					if _, err := netip.ParsePrefix(strings.TrimSpace(value)); err != nil {
						validationErr = fmt.Errorf("policy match CIDR value is invalid")
					}
				}
			}
		case "path_prefix":
			if len(args) == 2 {
				if value, ok := publicPolicyMatchStringLiteral(args[1]); ok {
					if !strings.HasPrefix(strings.TrimSpace(value), "/") {
						validationErr = fmt.Errorf("policy match path prefix must start with /")
					}
				}
			}
		case "host_match":
			if len(args) == 2 {
				if value, ok := publicPolicyMatchStringLiteral(args[1]); ok {
					if strings.TrimSpace(value) == "" {
						validationErr = fmt.Errorf("policy match host pattern is required")
					}
				}
			}
		case "matches":
			if call.IsMemberFunction() && len(args) == 1 {
				if value, ok := publicPolicyMatchStringLiteral(args[0]); ok {
					if _, err := regexp.Compile(value); err != nil {
						validationErr = fmt.Errorf("policy match regex value is invalid")
					}
				}
			}
		}
	})
	celast.PreOrderVisit(checked.NativeRep().Expr(), visitor)
	return validationErr
}

func publicPolicyMatchStringLiteral(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	value, ok := expr.AsLiteral().Value().(string)
	return value, ok
}

func (match publicPolicyMatchConfig) matches(listener publicListenerConfig, r *http.Request) bool {
	if program, ok := match.program.(cel.Program); ok && program != nil {
		return publicPolicyMatchProgramMatches(program, listener, r)
	}
	if strings.TrimSpace(match.CELExpression) != "" {
		compiled := match
		if err := compilePublicPolicyMatch(&compiled); err != nil {
			return false
		}
		if program, ok := compiled.program.(cel.Program); ok && program != nil {
			return publicPolicyMatchProgramMatches(program, listener, r)
		}
		return true
	}
	return true
}

func publicPolicyMatchProgramMatches(program cel.Program, listener publicListenerConfig, r *http.Request) bool {
	out, _, err := program.Eval(publicPolicyMatchActivation(listener, r))
	if err != nil {
		return false
	}
	value, ok := out.Value().(bool)
	return ok && value
}

func publicPolicyMatchActivation(listener publicListenerConfig, r *http.Request) map[string]any {
	headers := make(map[string][]string, len(r.Header))
	for name, values := range r.Header {
		key := strings.ToLower(name)
		headers[key] = append([]string(nil), values...)
	}
	cookies := make(map[string]string)
	for _, cookie := range r.Cookies() {
		if _, exists := cookies[cookie.Name]; !exists {
			cookies[cookie.Name] = cookie.Value
		}
	}
	query := make(map[string][]string, len(r.URL.Query()))
	for name, values := range r.URL.Query() {
		query[name] = append([]string(nil), values...)
	}
	return map[string]any{
		"method":    strings.ToUpper(r.Method),
		"protocol":  listener.Protocol,
		"host":      normalizeRequestHost(r.Host),
		"path":      r.URL.Path,
		"remote_ip": publicPolicyMatchRemoteIP(r.RemoteAddr),
		"headers":   headers,
		"cookies":   cookies,
		"query":     query,
	}
}

func publicPolicyMatchRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return host
	}
	return strings.Trim(strings.TrimSpace(remoteAddr), "[]")
}

func publicPolicyMatchConfigFromProto(rule *p2pstreamv1.PublicPolicyMatchRule) (publicPolicyMatchConfig, error) {
	if rule == nil {
		return publicPolicyMatchConfig{}, nil
	}
	builder, err := publicPolicyMatchBuilderFromProto(rule.Builder)
	if err != nil {
		return publicPolicyMatchConfig{}, err
	}
	config := publicPolicyMatchConfig{
		CELExpression: strings.TrimSpace(rule.CelExpression),
		Builder:       builder,
	}
	if config.Builder != nil {
		expr, err := publicPolicyMatchBuilderExpression(config.Builder)
		if err != nil {
			return publicPolicyMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if config.CELExpression == "" {
			config.CELExpression = expr
		} else if config.CELExpression != strings.TrimSpace(expr) {
			return publicPolicyMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match cel_expression does not match builder"))
		}
	}
	return config, nil
}

func publicPolicyMatchBuilderFromProto(builder *p2pstreamv1.PublicPolicyMatchBuilder) (*publicPolicyMatchBuilderConfig, error) {
	if builder == nil || builder.Root == nil {
		return nil, nil
	}
	count := 0
	root, err := publicPolicyMatchGroupFromProto(builder.Root, &count)
	if err != nil {
		return nil, err
	}
	return &publicPolicyMatchBuilderConfig{Root: root}, nil
}

func publicPolicyMatchGroupFromProto(group *p2pstreamv1.PublicPolicyMatchGroup, count *int) (*publicPolicyMatchGroupConfig, error) {
	if group == nil {
		return nil, nil
	}
	*count = *count + 1
	if *count > maxPublicPolicyMatchBuilderNodes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match builder has too many groups or conditions"))
	}
	operator, err := publicPolicyMatchBooleanOperatorFromProto(group.Operator)
	if err != nil {
		return nil, err
	}
	resp := &publicPolicyMatchGroupConfig{Operator: operator, Negated: group.Negated}
	for _, condition := range group.Conditions {
		*count = *count + 1
		if *count > maxPublicPolicyMatchBuilderNodes {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match builder has too many groups or conditions"))
		}
		item, err := publicPolicyMatchConditionFromProto(condition)
		if err != nil {
			return nil, err
		}
		resp.Conditions = append(resp.Conditions, item)
	}
	for _, child := range group.Groups {
		item, err := publicPolicyMatchGroupFromProto(child, count)
		if err != nil {
			return nil, err
		}
		if item != nil {
			resp.Groups = append(resp.Groups, *item)
		}
	}
	return resp, nil
}

func publicPolicyMatchConditionFromProto(condition *p2pstreamv1.PublicPolicyMatchCondition) (publicPolicyMatchConditionConfig, error) {
	if condition == nil {
		return publicPolicyMatchConditionConfig{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match condition is required"))
	}
	field, err := publicPolicyMatchFieldFromProto(condition.Field)
	if err != nil {
		return publicPolicyMatchConditionConfig{}, err
	}
	operator, err := publicPolicyMatchConditionOperatorFromProto(condition.Operator)
	if err != nil {
		return publicPolicyMatchConditionConfig{}, err
	}
	resp := publicPolicyMatchConditionConfig{
		Field:    field,
		Name:     strings.TrimSpace(condition.Name),
		Operator: operator,
		Negated:  condition.Negated,
	}
	if len(condition.Values) > maxPublicPolicyMatchConditionValues {
		return publicPolicyMatchConditionConfig{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match condition has too many values"))
	}
	resp.Values = append(resp.Values, condition.Values...)
	if err := normalizePublicPolicyMatchCondition(&resp); err != nil {
		return publicPolicyMatchConditionConfig{}, err
	}
	return resp, nil
}

func normalizePublicPolicyMatchCondition(condition *publicPolicyMatchConditionConfig) error {
	condition.Operator = strings.TrimSpace(condition.Operator)
	if !validPublicPolicyMatchOperator(condition.Operator) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match operator is invalid"))
	}
	switch condition.Field {
	case publicPolicyMatchFieldHeader:
		condition.Name = strings.ToLower(strings.TrimSpace(condition.Name))
		if condition.Name == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match header name is required"))
		}
		if !validHTTPToken(condition.Name) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match header name is invalid"))
		}
	case publicPolicyMatchFieldCookie, publicPolicyMatchFieldQueryParam:
		condition.Name = strings.TrimSpace(condition.Name)
		if condition.Name == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match attribute name is required"))
		}
	default:
		condition.Name = ""
	}
	if condition.Operator == publicPolicyMatchOperatorPresent {
		if condition.Field != publicPolicyMatchFieldHeader && condition.Field != publicPolicyMatchFieldCookie && condition.Field != publicPolicyMatchFieldQueryParam {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match present operator requires a header, cookie, or query parameter field"))
		}
		condition.Values = nil
		return nil
	}
	if len(condition.Values) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match condition value is required"))
	}
	for index, value := range condition.Values {
		if len(value) > maxPublicPolicyMatchValueBytes {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match condition value is too large"))
		}
		switch condition.Field {
		case publicPolicyMatchFieldMethod:
			value = strings.ToUpper(strings.TrimSpace(value))
			if value == "" || !validHTTPToken(value) {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match method value is invalid"))
			}
		case publicPolicyMatchFieldProtocol:
			value = strings.ToLower(strings.TrimSpace(value))
			if value != publicListenerProtocolHTTP && value != publicListenerProtocolHTTPS {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match protocol value is invalid"))
			}
		case publicPolicyMatchFieldHost:
			if condition.Operator == publicPolicyMatchOperatorHostPattern {
				value = normalizeHostPattern(value)
			} else {
				value = strings.ToLower(strings.TrimSpace(value))
			}
			if value == "" {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match host value is required"))
			}
		case publicPolicyMatchFieldPath:
			value = strings.TrimSpace(value)
			if value == "" {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match path value is required"))
			}
			if condition.Operator == publicPolicyMatchOperatorPrefix && !strings.HasPrefix(value, "/") {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match path prefix must start with /"))
			}
		case publicPolicyMatchFieldRemoteIP:
			value = strings.TrimSpace(value)
			if condition.Operator == publicPolicyMatchOperatorCIDR {
				if _, err := netip.ParsePrefix(value); err != nil {
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match CIDR value is invalid"))
				}
			}
		}
		if condition.Operator == publicPolicyMatchOperatorMatches {
			if _, err := regexp.Compile(value); err != nil {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match regex value is invalid"))
			}
		}
		condition.Values[index] = value
	}
	switch condition.Operator {
	case publicPolicyMatchOperatorCIDR:
		if condition.Field != publicPolicyMatchFieldRemoteIP {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match CIDR operator requires remote IP field"))
		}
	case publicPolicyMatchOperatorHostPattern:
		if condition.Field != publicPolicyMatchFieldHost {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match host pattern operator requires host field"))
		}
	}
	return nil
}

func validPublicPolicyMatchOperator(operator string) bool {
	switch operator {
	case publicPolicyMatchOperatorPresent,
		publicPolicyMatchOperatorEquals,
		publicPolicyMatchOperatorPrefix,
		publicPolicyMatchOperatorSuffix,
		publicPolicyMatchOperatorContains,
		publicPolicyMatchOperatorMatches,
		publicPolicyMatchOperatorIn,
		publicPolicyMatchOperatorCIDR,
		publicPolicyMatchOperatorHostPattern:
		return true
	default:
		return false
	}
}

func publicPolicyMatchBuilderExpression(builder *publicPolicyMatchBuilderConfig) (string, error) {
	if builder == nil || builder.Root == nil {
		return "", nil
	}
	count := 0
	return publicPolicyMatchGroupExpression(*builder.Root, &count)
}

func publicPolicyMatchGroupExpression(group publicPolicyMatchGroupConfig, count *int) (string, error) {
	*count = *count + 1
	if *count > maxPublicPolicyMatchBuilderNodes {
		return "", fmt.Errorf("policy match builder has too many groups or conditions")
	}
	operator := strings.TrimSpace(group.Operator)
	if operator == "" {
		operator = publicPolicyMatchBooleanAll
	}
	if operator != publicPolicyMatchBooleanAll && operator != publicPolicyMatchBooleanAny {
		return "", fmt.Errorf("policy match boolean operator is invalid")
	}
	parts := make([]string, 0, len(group.Conditions)+len(group.Groups))
	for _, condition := range group.Conditions {
		*count = *count + 1
		if *count > maxPublicPolicyMatchBuilderNodes {
			return "", fmt.Errorf("policy match builder has too many groups or conditions")
		}
		item := condition
		if err := normalizePublicPolicyMatchCondition(&item); err != nil {
			return "", err
		}
		expr, err := publicPolicyMatchConditionExpression(item)
		if err != nil {
			return "", err
		}
		parts = append(parts, expr)
	}
	for _, child := range group.Groups {
		expr, err := publicPolicyMatchGroupExpression(child, count)
		if err != nil {
			return "", err
		}
		parts = append(parts, expr)
	}
	expr := "true"
	if len(parts) > 0 {
		joiner := " && "
		if operator == publicPolicyMatchBooleanAny {
			joiner = " || "
		}
		expr = "(" + strings.Join(parts, joiner) + ")"
	}
	if group.Negated {
		expr = "!(" + expr + ")"
	}
	return expr, nil
}

func publicPolicyMatchConditionExpression(condition publicPolicyMatchConditionConfig) (string, error) {
	var expr string
	switch condition.Field {
	case publicPolicyMatchFieldHeader:
		expr = publicPolicyMatchRepeatedFieldExpression("headers", condition)
	case publicPolicyMatchFieldQueryParam:
		expr = publicPolicyMatchRepeatedFieldExpression("query", condition)
	case publicPolicyMatchFieldCookie:
		expr = publicPolicyMatchMapStringFieldExpression("cookies", condition)
	case publicPolicyMatchFieldRemoteIP:
		if condition.Operator == publicPolicyMatchOperatorCIDR {
			expr = publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
				return "cidr(remote_ip, " + publicPolicyMatchCELQuote(value) + ")"
			})
		} else {
			expr = publicPolicyMatchScalarFieldExpression("remote_ip", condition)
		}
	case publicPolicyMatchFieldHost:
		if condition.Operator == publicPolicyMatchOperatorHostPattern {
			expr = publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
				return "host_match(host, " + publicPolicyMatchCELQuote(value) + ")"
			})
		} else {
			expr = publicPolicyMatchScalarFieldExpression("host", condition)
		}
	case publicPolicyMatchFieldPath:
		if condition.Operator == publicPolicyMatchOperatorPrefix {
			expr = publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
				return "path_prefix(path, " + publicPolicyMatchCELQuote(value) + ")"
			})
		} else {
			expr = publicPolicyMatchScalarFieldExpression("path", condition)
		}
	case publicPolicyMatchFieldMethod:
		expr = publicPolicyMatchScalarFieldExpression("method", condition)
	case publicPolicyMatchFieldProtocol:
		expr = publicPolicyMatchScalarFieldExpression("protocol", condition)
	default:
		return "", fmt.Errorf("policy match field is invalid")
	}
	if condition.Negated {
		expr = "!(" + expr + ")"
	}
	return "(" + expr + ")", nil
}

func publicPolicyMatchScalarFieldExpression(field string, condition publicPolicyMatchConditionConfig) string {
	switch condition.Operator {
	case publicPolicyMatchOperatorIn:
		return field + " in " + publicPolicyMatchCELStringList(condition.Values)
	default:
		return publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
			return publicPolicyMatchStringComparison(field, condition.Operator, value)
		})
	}
}

func publicPolicyMatchMapStringFieldExpression(mapName string, condition publicPolicyMatchConditionConfig) string {
	name := publicPolicyMatchCELQuote(condition.Name)
	present := name + " in " + mapName
	if condition.Operator == publicPolicyMatchOperatorPresent {
		return present
	}
	valueExpr := mapName + "[" + name + "]"
	var comparison string
	if condition.Operator == publicPolicyMatchOperatorIn {
		comparison = valueExpr + " in " + publicPolicyMatchCELStringList(condition.Values)
	} else {
		comparison = publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
			return publicPolicyMatchStringComparison(valueExpr, condition.Operator, value)
		})
	}
	return "(" + present + " && (" + comparison + "))"
}

func publicPolicyMatchRepeatedFieldExpression(mapName string, condition publicPolicyMatchConditionConfig) string {
	name := publicPolicyMatchCELQuote(condition.Name)
	present := name + " in " + mapName
	if condition.Operator == publicPolicyMatchOperatorPresent {
		return present
	}
	var comparison string
	if condition.Operator == publicPolicyMatchOperatorIn {
		comparison = "v in " + publicPolicyMatchCELStringList(condition.Values)
	} else {
		comparison = publicPolicyMatchAnyValueExpression(condition.Values, func(value string) string {
			return publicPolicyMatchStringComparison("v", condition.Operator, value)
		})
	}
	return "(" + present + " && " + mapName + "[" + name + "].exists(v, " + comparison + "))"
}

func publicPolicyMatchStringComparison(lhs string, operator string, value string) string {
	quoted := publicPolicyMatchCELQuote(value)
	switch operator {
	case publicPolicyMatchOperatorEquals:
		return lhs + " == " + quoted
	case publicPolicyMatchOperatorPrefix:
		return lhs + ".startsWith(" + quoted + ")"
	case publicPolicyMatchOperatorSuffix:
		return lhs + ".endsWith(" + quoted + ")"
	case publicPolicyMatchOperatorContains:
		return lhs + ".contains(" + quoted + ")"
	case publicPolicyMatchOperatorMatches:
		return lhs + ".matches(" + quoted + ")"
	default:
		return "false"
	}
}

func publicPolicyMatchAnyValueExpression(values []string, expr func(string) string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, expr(value))
	}
	if len(parts) == 0 {
		return "false"
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

func publicPolicyMatchCELQuote(value string) string {
	return strconv.Quote(value)
}

func publicPolicyMatchCELStringList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, publicPolicyMatchCELQuote(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func publicPolicyMatchRuleToProto(match publicPolicyMatchConfig) *p2pstreamv1.PublicPolicyMatchRule {
	expression := strings.TrimSpace(match.CELExpression)
	builder := publicPolicyMatchBuilderToProto(match.Builder)
	if expression == "" && builder == nil {
		return nil
	}
	return &p2pstreamv1.PublicPolicyMatchRule{
		CelExpression: expression,
		Builder:       builder,
	}
}

func publicPolicyMatchBuilderToProto(builder *publicPolicyMatchBuilderConfig) *p2pstreamv1.PublicPolicyMatchBuilder {
	if builder == nil || builder.Root == nil {
		return nil
	}
	return &p2pstreamv1.PublicPolicyMatchBuilder{Root: publicPolicyMatchGroupToProto(*builder.Root)}
}

func publicPolicyMatchGroupToProto(group publicPolicyMatchGroupConfig) *p2pstreamv1.PublicPolicyMatchGroup {
	resp := &p2pstreamv1.PublicPolicyMatchGroup{
		Operator: publicPolicyMatchBooleanOperatorToProto(group.Operator),
		Negated:  group.Negated,
	}
	for _, condition := range group.Conditions {
		resp.Conditions = append(resp.Conditions, publicPolicyMatchConditionToProto(condition))
	}
	for _, child := range group.Groups {
		resp.Groups = append(resp.Groups, publicPolicyMatchGroupToProto(child))
	}
	return resp
}

func publicPolicyMatchConditionToProto(condition publicPolicyMatchConditionConfig) *p2pstreamv1.PublicPolicyMatchCondition {
	return &p2pstreamv1.PublicPolicyMatchCondition{
		Field:    publicPolicyMatchFieldToProto(condition.Field),
		Name:     condition.Name,
		Operator: publicPolicyMatchConditionOperatorToProto(condition.Operator),
		Values:   append([]string(nil), condition.Values...),
		Negated:  condition.Negated,
	}
}

func publicPolicyMatchBooleanOperatorFromProto(operator p2pstreamv1.PublicPolicyMatchBooleanOperator) (string, error) {
	switch operator {
	case p2pstreamv1.PublicPolicyMatchBooleanOperator_PUBLIC_POLICY_MATCH_BOOLEAN_OPERATOR_UNSPECIFIED,
		p2pstreamv1.PublicPolicyMatchBooleanOperator_PUBLIC_POLICY_MATCH_BOOLEAN_OPERATOR_ALL:
		return publicPolicyMatchBooleanAll, nil
	case p2pstreamv1.PublicPolicyMatchBooleanOperator_PUBLIC_POLICY_MATCH_BOOLEAN_OPERATOR_ANY:
		return publicPolicyMatchBooleanAny, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match boolean operator is invalid"))
	}
}

func publicPolicyMatchBooleanOperatorToProto(operator string) p2pstreamv1.PublicPolicyMatchBooleanOperator {
	if operator == publicPolicyMatchBooleanAny {
		return p2pstreamv1.PublicPolicyMatchBooleanOperator_PUBLIC_POLICY_MATCH_BOOLEAN_OPERATOR_ANY
	}
	return p2pstreamv1.PublicPolicyMatchBooleanOperator_PUBLIC_POLICY_MATCH_BOOLEAN_OPERATOR_ALL
}

func publicPolicyMatchFieldFromProto(field p2pstreamv1.PublicPolicyMatchField) (string, error) {
	switch field {
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_METHOD:
		return publicPolicyMatchFieldMethod, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_PROTOCOL:
		return publicPolicyMatchFieldProtocol, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_HOST:
		return publicPolicyMatchFieldHost, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_PATH:
		return publicPolicyMatchFieldPath, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_REMOTE_IP:
		return publicPolicyMatchFieldRemoteIP, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_HEADER:
		return publicPolicyMatchFieldHeader, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_COOKIE:
		return publicPolicyMatchFieldCookie, nil
	case p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_QUERY_PARAM:
		return publicPolicyMatchFieldQueryParam, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match field is invalid"))
	}
}

func publicPolicyMatchFieldToProto(field string) p2pstreamv1.PublicPolicyMatchField {
	switch field {
	case publicPolicyMatchFieldMethod:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_METHOD
	case publicPolicyMatchFieldProtocol:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_PROTOCOL
	case publicPolicyMatchFieldHost:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_HOST
	case publicPolicyMatchFieldPath:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_PATH
	case publicPolicyMatchFieldRemoteIP:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_REMOTE_IP
	case publicPolicyMatchFieldHeader:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_HEADER
	case publicPolicyMatchFieldCookie:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_COOKIE
	case publicPolicyMatchFieldQueryParam:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_QUERY_PARAM
	default:
		return p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_UNSPECIFIED
	}
}

func publicPolicyMatchConditionOperatorFromProto(operator p2pstreamv1.PublicPolicyMatchConditionOperator) (string, error) {
	switch operator {
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_PRESENT:
		return publicPolicyMatchOperatorPresent, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_EQUALS:
		return publicPolicyMatchOperatorEquals, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_PREFIX:
		return publicPolicyMatchOperatorPrefix, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_SUFFIX:
		return publicPolicyMatchOperatorSuffix, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_CONTAINS:
		return publicPolicyMatchOperatorContains, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_MATCHES:
		return publicPolicyMatchOperatorMatches, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_IN:
		return publicPolicyMatchOperatorIn, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_CIDR:
		return publicPolicyMatchOperatorCIDR, nil
	case p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_HOST_PATTERN:
		return publicPolicyMatchOperatorHostPattern, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy match condition operator is invalid"))
	}
}

func publicPolicyMatchConditionOperatorToProto(operator string) p2pstreamv1.PublicPolicyMatchConditionOperator {
	switch operator {
	case publicPolicyMatchOperatorPresent:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_PRESENT
	case publicPolicyMatchOperatorPrefix:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_PREFIX
	case publicPolicyMatchOperatorSuffix:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_SUFFIX
	case publicPolicyMatchOperatorContains:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_CONTAINS
	case publicPolicyMatchOperatorMatches:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_MATCHES
	case publicPolicyMatchOperatorIn:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_IN
	case publicPolicyMatchOperatorCIDR:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_CIDR
	case publicPolicyMatchOperatorHostPattern:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_HOST_PATTERN
	default:
		return p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_EQUALS
	}
}
