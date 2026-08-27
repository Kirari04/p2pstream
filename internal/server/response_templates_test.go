package server

import (
	htmltemplate "html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestPublicResponseTemplateValidationRejectsInvalidWafPlaceholders(t *testing.T) {
	cases := []struct {
		name string
		kind p2pstreamv1.PublicResponseTemplateKind
		body string
	}{
		{
			name: "missing-captcha",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
			body: `<html>{{ .host }}</html>`,
		},
		{
			name: "unknown-placeholder",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
			body: `<html>{{ .captcha_element_html }} {{ .not_allowed }}</html>`,
		},
		{
			name: "missing-waiting-room",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE,
			body: `<html>{{ .queue_position }}</html>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePublicResponseTemplateInput(tc.name, tc.kind, "", "", tc.body)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("expected invalid template error, got %v", err)
			}
		})
	}
}

func TestPublicResponseTemplateValidationAcceptsRequiredWafPlaceholders(t *testing.T) {
	captcha, err := validatePublicResponseTemplateInput(
		"captcha-template",
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
		"",
		"",
		`<html>{{ .page_title }} {{ .captcha_element_html }}</html>`,
	)
	if err != nil {
		t.Fatalf("captcha template rejected: %v", err)
	}
	if captcha.Kind != publicResponseTemplateKindWafCaptchaPage {
		t.Fatalf("captcha kind = %q", captcha.Kind)
	}

	waiting, err := validatePublicResponseTemplateInput(
		"waiting-template",
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE,
		"",
		"",
		`<html>{{ .queue_position }} {{ .retry_after_seconds }} {{ .status_url }}</html>`,
	)
	if err != nil {
		t.Fatalf("waiting-room template rejected: %v", err)
	}
	if waiting.Kind != publicResponseTemplateKindWafWaitingRoomPage {
		t.Fatalf("waiting kind = %q", waiting.Kind)
	}
}

func TestPublicResponseTemplateValidationRequiresFunctionalLocalLoginFields(t *testing.T) {
	valid := `<form action="{{ .login_action }}"><input name="{{ .csrf_field_name }}" value="{{ .csrf_token }}"><input name="{{ .username_field_name }}"><input name="{{ .password_field_name }}"></form>`
	input, err := validatePublicResponseTemplateInput(
		"local-sign-in",
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_LOCAL_ACCESS_LOGIN_PAGE,
		"",
		"text/html; charset=utf-8",
		valid,
	)
	if err != nil {
		t.Fatalf("local sign-in template rejected: %v", err)
	}
	if input.Kind != publicResponseTemplateKindLocalAccessLoginPage {
		t.Fatalf("local sign-in kind = %q", input.Kind)
	}

	for name, body := range map[string]string{
		"missing CSRF token":   strings.Replace(valid, ` value="{{ .csrf_token }}"`, "", 1),
		"unknown field":        valid + `{{ .redirect_url }}`,
		"unknown nested field": valid + `{{ define "extra" }}{{ .redirect_url }}{{ end }}{{ template "extra" . }}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validatePublicResponseTemplateInput(
				"local-sign-in",
				p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_LOCAL_ACCESS_LOGIN_PAGE,
				"",
				"text/html; charset=utf-8",
				body,
			)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("expected invalid template error, got %v", err)
			}
		})
	}

	if _, err := validatePublicResponseTemplateInput(
		defaultLocalAccessLoginTemplateName,
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_LOCAL_ACCESS_LOGIN_PAGE,
		"",
		defaultResponseTemplateContentType,
		defaultLocalAccessLoginBody,
	); err != nil {
		t.Fatalf("embedded local sign-in template rejected: %v", err)
	}
}

func TestPublicLocalLoginTemplateEscapesAttackerControlledValues(t *testing.T) {
	const source = `CUSTOM {{ .host }} {{ .provider_name }} {{ .error_message }}<form action="{{ .login_action }}"><input name="{{ .csrf_field_name }}" value="{{ .csrf_token }}"><input name="{{ .username_field_name }}" value="{{ .username }}"><input name="{{ .password_field_name }}"></form>`
	tmpl, err := htmltemplate.New("login").Parse(source)
	if err != nil {
		t.Fatalf("parse custom login template: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://safe.example/private?next=1", nil)
	req.Host = `bad.example"><script>alert(1)</script>`
	recorder := httptest.NewRecorder()
	ctx := &publicProxyContext{
		ResponseWriter: recorder,
		Request:        req,
		RouteMatch: publicRouteMatch{Listener: publicListenerConfig{
			Protocol: publicListenerProtocolHTTP,
		}},
	}
	provider := publicAccessProviderConfig{
		ID:                                42,
		Name:                              `Private <script>alert(2)</script>`,
		LocalAuthLoginTemplate:            tmpl,
		LocalAuthLoginTemplateContentType: "text/html; charset=utf-8",
	}
	if err := writePublicLocalLoginForm(ctx, provider, `alice"><img src=x onerror=alert(3)>`, `<script>alert(4)</script>`, http.StatusUnauthorized); err != nil {
		t.Fatalf("render custom login template: %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "CUSTOM ") || strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Fatalf("custom template did not escape dynamic values:\n%s", body)
	}
	for _, escaped := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;script&gt;alert(2)&lt;/script&gt;", "&lt;script&gt;alert(4)&lt;/script&gt;", "&lt;img src=x onerror=alert(3)&gt;"} {
		if !strings.Contains(body, escaped) {
			t.Fatalf("rendered body missing escaped value %q:\n%s", escaped, body)
		}
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self'") || !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("content security policy = %q", got)
	}
}

func TestPublicWafTemplateRenderEscapesNormalValuesAndTrustsCaptchaElement(t *testing.T) {
	var out strings.Builder
	err := renderPublicHTMLTemplate(&out, `<main>{{ .host }} {{ .captcha_element_html }}</main>`, map[string]any{
		"host":                 `bad.example"><script>alert(1)</script>`,
		"captcha_element_html": htmltemplate.HTML(`<form><div class="cf-turnstile" data-sitekey="preview"></div></form>`),
	})
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `bad.example&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Fatalf("normal placeholder was not escaped\n%s", body)
	}
	if !strings.Contains(body, `<form><div class="cf-turnstile"`) {
		t.Fatalf("captcha element was escaped or missing\n%s", body)
	}
}
