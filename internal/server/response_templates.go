package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"mime"
	"strings"
	"text/template/parse"
	"unicode/utf8"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	maxGenericResponseTemplateBodyBytes = 64 * 1024
	maxWafPageTemplateBodyBytes         = 128 * 1024
)

type publicResponseTemplateConfig struct {
	ID          int64
	Name        string
	Kind        string
	Description string
	ContentType string
	Body        string
}

type publicResponseTemplateMutationInput struct {
	Name        string
	Kind        string
	Description string
	ContentType string
	Body        string
}

type defaultPublicResponseTemplateSeed struct {
	Name        string
	Kind        string
	Description string
	ContentType string
	Body        string
}

var defaultPublicResponseTemplates = []defaultPublicResponseTemplateSeed{
	{
		Name:        "default-welcome",
		Kind:        publicResponseTemplateKindGenericBody,
		Description: "Default static welcome page.",
		ContentType: defaultWelcomeContentType,
		Body:        defaultWelcomeBody,
	},
	{
		Name:        "rate-limit-default",
		Kind:        publicResponseTemplateKindGenericBody,
		Description: "Default rate-limit denial body.",
		ContentType: defaultRateLimitContentType,
		Body:        defaultRateLimitBody,
	},
	{
		Name:        "waf-block-default",
		Kind:        publicResponseTemplateKindGenericBody,
		Description: "Default WAF block body.",
		ContentType: defaultWafBlockContentType,
		Body:        defaultWafBlockBody,
	},
	{
		Name:        "waf-captcha-default",
		Kind:        publicResponseTemplateKindWafCaptchaPage,
		Description: "Starter WAF captcha challenge page.",
		ContentType: defaultResponseTemplateContentType,
		Body: `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>{{ .host }} needs to verify your connection</h1>
    <p>{{ .page_body }}</p>
    {{ .captcha_element_html }}
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
`,
	},
	{
		Name:        "waf-waiting-room-default",
		Kind:        publicResponseTemplateKindWafWaitingRoomPage,
		Description: "Starter WAF waiting-room page.",
		ContentType: defaultResponseTemplateContentType,
		Body: `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta http-equiv="refresh" content="{{ .retry_after_seconds }}">
  <title>{{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>{{ .page_title }}</h1>
    <p>{{ .page_body }}</p>
    <dl>
      <dt>Queue position</dt>
      <dd>{{ .queue_position }}</dd>
      <dt>Next check</dt>
      <dd>{{ .retry_after_seconds }}s</dd>
    </dl>
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
`,
	},
}

func (a *App) CreatePublicResponseTemplate(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicResponseTemplateRequest]) (*connect.Response[p2pstreamv1.CreatePublicResponseTemplateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	input, err := validatePublicResponseTemplateInput(req.Msg.Name, req.Msg.Kind, req.Msg.Description, req.Msg.ContentType, req.Msg.Body)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicResponseTemplate(ctx, db.CreatePublicResponseTemplateParams{
		Name:        input.Name,
		Kind:        input.Kind,
		Description: input.Description,
		ContentType: input.ContentType,
		Body:        input.Body,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicResponseTemplateResponse{Template: publicResponseTemplateToProto(row)}), nil
}

func (a *App) UpdatePublicResponseTemplate(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicResponseTemplateRequest]) (*connect.Response[p2pstreamv1.UpdatePublicResponseTemplateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	current, err := a.DB.GetPublicResponseTemplate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	input, err := validatePublicResponseTemplateInput(req.Msg.Name, req.Msg.Kind, req.Msg.Description, req.Msg.ContentType, req.Msg.Body)
	if err != nil {
		return nil, err
	}
	if current.Kind != input.Kind {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("response template kind cannot be changed"))
	}
	row, err := a.DB.UpdatePublicResponseTemplate(ctx, db.UpdatePublicResponseTemplateParams{
		ID:          req.Msg.Id,
		Name:        input.Name,
		Kind:        input.Kind,
		Description: input.Description,
		ContentType: input.ContentType,
		Body:        input.Body,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicResponseTemplateResponse{Template: publicResponseTemplateToProto(row)}), nil
}

func (a *App) DeletePublicResponseTemplate(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicResponseTemplateRequest]) (*connect.Response[p2pstreamv1.DeletePublicResponseTemplateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicResponseTemplate(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicResponseTemplateResponse{}), nil
}

func validatePublicResponseTemplateInput(name string, kind p2pstreamv1.PublicResponseTemplateKind, description string, contentType string, body string) (publicResponseTemplateMutationInput, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return publicResponseTemplateMutationInput{}, err
	}
	kindString, err := publicResponseTemplateKindStringFromProto(kind)
	if err != nil {
		return publicResponseTemplateMutationInput{}, err
	}
	description = strings.TrimSpace(description)
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = defaultResponseTemplateContentType
	}
	if !utf8.ValidString(body) {
		return publicResponseTemplateMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("template body must be valid UTF-8"))
	}
	limit := maxGenericResponseTemplateBodyBytes
	if kindString != publicResponseTemplateKindGenericBody {
		limit = maxWafPageTemplateBodyBytes
	}
	if len(body) > limit {
		return publicResponseTemplateMutationInput{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template body must be at most %d bytes", limit))
	}
	if kindString != publicResponseTemplateKindGenericBody {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return publicResponseTemplateMutationInput{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template content type is invalid: %w", err))
		}
		if !strings.EqualFold(mediaType, "text/html") && !strings.EqualFold(mediaType, "application/xhtml+xml") {
			return publicResponseTemplateMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF page templates must use an HTML content type"))
		}
		if err := validateWafPageTemplate(kindString, body); err != nil {
			return publicResponseTemplateMutationInput{}, err
		}
	}
	return publicResponseTemplateMutationInput{
		Name:        name,
		Kind:        kindString,
		Description: description,
		ContentType: contentType,
		Body:        body,
	}, nil
}

func validateWafPageTemplate(kind string, body string) error {
	tmpl, err := htmltemplate.New("response-template").Parse(body)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template HTML is invalid: %w", err))
	}
	fields := map[string]bool{}
	collectTemplateFields(tmpl.Tree.Root, fields)
	allowed := publicWafTemplateAllowedFields(kind)
	for field := range fields {
		if !allowed[field] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template placeholder %q is not allowed for %s templates", field, kind))
		}
	}
	for _, required := range publicWafTemplateRequiredFields(kind) {
		if !fields[required] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template placeholder %q is required for %s templates", required, kind))
		}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, sampleWafTemplateData(kind)); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("template sample render failed: %w", err))
	}
	return nil
}

func collectTemplateFields(node parse.Node, fields map[string]bool) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *parse.ListNode:
		for _, child := range n.Nodes {
			collectTemplateFields(child, fields)
		}
	case *parse.ActionNode:
		collectTemplateFields(n.Pipe, fields)
	case *parse.IfNode:
		collectTemplateFields(n.Pipe, fields)
		collectTemplateFields(n.List, fields)
		collectTemplateFields(n.ElseList, fields)
	case *parse.RangeNode:
		collectTemplateFields(n.Pipe, fields)
		collectTemplateFields(n.List, fields)
		collectTemplateFields(n.ElseList, fields)
	case *parse.WithNode:
		collectTemplateFields(n.Pipe, fields)
		collectTemplateFields(n.List, fields)
		collectTemplateFields(n.ElseList, fields)
	case *parse.TemplateNode:
		collectTemplateFields(n.Pipe, fields)
	case *parse.PipeNode:
		for _, cmd := range n.Cmds {
			collectTemplateFields(cmd, fields)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			collectTemplateFields(arg, fields)
		}
	case *parse.FieldNode:
		if len(n.Ident) > 0 {
			fields[n.Ident[0]] = true
		}
	case *parse.ChainNode:
		if len(n.Field) > 0 {
			fields[n.Field[0]] = true
		}
	}
}

func publicWafTemplateAllowedFields(kind string) map[string]bool {
	fields := map[string]bool{
		"host":         true,
		"rule_name":    true,
		"reference_id": true,
		"page_title":   true,
		"page_body":    true,
		"status_url":   true,
	}
	switch kind {
	case publicResponseTemplateKindWafCaptchaPage:
		fields["captcha_element_html"] = true
	case publicResponseTemplateKindWafWaitingRoomPage:
		fields["queue_position"] = true
		fields["retry_after_seconds"] = true
	}
	return fields
}

func publicWafTemplateRequiredFields(kind string) []string {
	switch kind {
	case publicResponseTemplateKindWafCaptchaPage:
		return []string{"captcha_element_html"}
	case publicResponseTemplateKindWafWaitingRoomPage:
		return []string{"queue_position", "retry_after_seconds"}
	default:
		return nil
	}
}

func sampleWafTemplateData(kind string) map[string]any {
	data := map[string]any{
		"host":         "app.example.test",
		"rule_name":    "waf-rule",
		"reference_id": "waf-1-preview",
		"page_title":   "Security check",
		"page_body":    "Traffic is being verified before continuing.",
		"status_url":   publicWafWaitingRoomStatusPath,
	}
	if kind == publicResponseTemplateKindWafCaptchaPage {
		data["captcha_element_html"] = htmltemplate.HTML(`<form><div class="cf-turnstile" data-sitekey="preview"></div><button type="submit">Continue</button></form>`)
	}
	if kind == publicResponseTemplateKindWafWaitingRoomPage {
		data["queue_position"] = "12"
		data["retry_after_seconds"] = "5"
	}
	return data
}

func publicResponseTemplateKindStringFromProto(kind p2pstreamv1.PublicResponseTemplateKind) (string, error) {
	switch kind {
	case p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY:
		return publicResponseTemplateKindGenericBody, nil
	case p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE:
		return publicResponseTemplateKindWafCaptchaPage, nil
	case p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE:
		return publicResponseTemplateKindWafWaitingRoomPage, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("response template kind is invalid"))
	}
}

func protoPublicResponseTemplateKind(kind string) p2pstreamv1.PublicResponseTemplateKind {
	switch strings.TrimSpace(kind) {
	case publicResponseTemplateKindGenericBody:
		return p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_GENERIC_BODY
	case publicResponseTemplateKindWafCaptchaPage:
		return p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE
	case publicResponseTemplateKindWafWaitingRoomPage:
		return p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE
	default:
		return p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_UNSPECIFIED
	}
}

func publicResponseBodyModeStringFromProto(mode p2pstreamv1.PublicResponseBodyMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_UNSPECIFIED,
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE:
		return publicResponseBodyModeInline, nil
	case p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_TEMPLATE:
		return publicResponseBodyModeTemplate, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("response body mode is invalid"))
	}
}

func protoPublicResponseBodyMode(mode string) p2pstreamv1.PublicResponseBodyMode {
	switch strings.TrimSpace(mode) {
	case publicResponseBodyModeTemplate:
		return p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_TEMPLATE
	case publicResponseBodyModeInline:
		return p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE
	default:
		return p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_UNSPECIFIED
	}
}

func normalizePublicResponseBodyMode(mode string) string {
	if strings.TrimSpace(mode) == publicResponseBodyModeTemplate {
		return publicResponseBodyModeTemplate
	}
	return publicResponseBodyModeInline
}

func publicResponseTemplatesToConfig(rows []db.PublicResponseTemplate) map[int64]publicResponseTemplateConfig {
	templates := make(map[int64]publicResponseTemplateConfig, len(rows))
	for _, row := range rows {
		templates[row.ID] = publicResponseTemplateConfig{
			ID:          row.ID,
			Name:        row.Name,
			Kind:        row.Kind,
			Description: row.Description,
			ContentType: row.ContentType,
			Body:        row.Body,
		}
	}
	return templates
}

func publicResponseTemplatesToProto(rows []db.PublicResponseTemplate) []*p2pstreamv1.PublicResponseTemplate {
	resp := make([]*p2pstreamv1.PublicResponseTemplate, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, publicResponseTemplateToProto(row))
	}
	return resp
}

func publicResponseTemplateToProto(row db.PublicResponseTemplate) *p2pstreamv1.PublicResponseTemplate {
	return &p2pstreamv1.PublicResponseTemplate{
		Id:                  row.ID,
		Name:                row.Name,
		Kind:                protoPublicResponseTemplateKind(row.Kind),
		Description:         row.Description,
		ContentType:         row.ContentType,
		Body:                row.Body,
		CreatedAtUnixMillis: row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: row.UpdatedAt.UnixMilli(),
	}
}

func (a *App) validateGenericResponseTemplateReference(ctx context.Context, mode p2pstreamv1.PublicResponseBodyMode, templateID int64) (string, sql.NullInt64, error) {
	modeString, err := publicResponseBodyModeStringFromProto(mode)
	if err != nil {
		return "", sql.NullInt64{}, err
	}
	if modeString == publicResponseBodyModeInline {
		return modeString, sql.NullInt64{}, nil
	}
	ref, err := a.validatePublicResponseTemplateReference(ctx, templateID, publicResponseTemplateKindGenericBody)
	return modeString, ref, err
}

func (a *App) validatePublicResponseTemplateReference(ctx context.Context, templateID int64, kind string) (sql.NullInt64, error) {
	if templateID <= 0 {
		return sql.NullInt64{}, connect.NewError(connect.CodeInvalidArgument, errors.New("response template id is required"))
	}
	row, err := a.DB.GetPublicResponseTemplate(ctx, templateID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, connect.NewError(connect.CodeNotFound, errors.New("response template not found"))
		}
		return sql.NullInt64{}, connect.NewError(connect.CodeInternal, err)
	}
	if row.Kind != kind {
		return sql.NullInt64{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("response template %q has kind %s, want %s", row.Name, row.Kind, kind))
	}
	return sql.NullInt64{Int64: templateID, Valid: true}, nil
}

func effectiveGenericResponseBody(mode string, templateID sql.NullInt64, inlineBody string, templates map[int64]publicResponseTemplateConfig) (string, error) {
	if normalizePublicResponseBodyMode(mode) != publicResponseBodyModeTemplate {
		return inlineBody, nil
	}
	if !templateID.Valid || templateID.Int64 <= 0 {
		return "", errors.New("template response body mode requires a response template")
	}
	template, ok := templates[templateID.Int64]
	if !ok {
		return "", fmt.Errorf("response template %d not found", templateID.Int64)
	}
	if template.Kind != publicResponseTemplateKindGenericBody {
		return "", fmt.Errorf("response template %q has kind %s, want %s", template.Name, template.Kind, publicResponseTemplateKindGenericBody)
	}
	return template.Body, nil
}

func optionalWafPageTemplate(templateID sql.NullInt64, kind string, templates map[int64]publicResponseTemplateConfig) (string, error) {
	if !templateID.Valid || templateID.Int64 <= 0 {
		return "", nil
	}
	template, ok := templates[templateID.Int64]
	if !ok {
		return "", fmt.Errorf("response template %d not found", templateID.Int64)
	}
	if template.Kind != kind {
		return "", fmt.Errorf("response template %q has kind %s, want %s", template.Name, template.Kind, kind)
	}
	return template.Body, nil
}

func renderPublicWafHTMLTemplate(w io.Writer, body string, data map[string]any) error {
	tmpl, err := htmltemplate.New("waf-response-template").Parse(body)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, data)
}

func (a *App) ensureDefaultPublicResponseTemplates(ctx context.Context) (map[string]db.PublicResponseTemplate, error) {
	seeded := make(map[string]db.PublicResponseTemplate, len(defaultPublicResponseTemplates))
	for _, seed := range defaultPublicResponseTemplates {
		row, err := a.DB.GetPublicResponseTemplateByName(ctx, seed.Name)
		if err == nil {
			seeded[seed.Name] = row
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		row, err = a.DB.CreatePublicResponseTemplate(ctx, db.CreatePublicResponseTemplateParams{
			Name:        seed.Name,
			Kind:        seed.Kind,
			Description: seed.Description,
			ContentType: seed.ContentType,
			Body:        seed.Body,
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
				row, err = a.DB.GetPublicResponseTemplateByName(ctx, seed.Name)
			}
			if err != nil {
				return nil, publicDBError(err)
			}
		}
		seeded[seed.Name] = row
	}
	return seeded, nil
}
