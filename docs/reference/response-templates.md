# Response Templates Reference

Response templates are centrally managed response bodies and visitor-facing pages. They can be reused by static route targets, rate-limit responses, WAF responses, and built-in local-access providers while keeping inline bodies available for one-off cases.

## Template Kinds

| Kind | Use | Runtime behavior |
| --- | --- | --- |
| Generic body | Static target bodies, rate-limit response bodies, and WAF block bodies. | The body is reused exactly as stored. Placeholders are not rendered. |
| WAF captcha page | Full HTML page for captcha WAF rules. | Rendered with `html/template` and sample-validated on save. |
| WAF waiting room page | Full HTML page for waiting-room WAF rules. | Rendered with `html/template` and sample-validated on save. |
| Local access sign-in page | Sign-in form plus expired-session, throttling, and invalid-credential states for a built-in local provider. | Rendered with `html/template`, selected per local provider, and sample-validated on save. |

Generic templates only replace the body text. The object that references the template still owns the HTTP status, content type, and headers. For example, a rate-limit rule still uses its configured response status and response content type even when the body comes from a template.

## Create And Edit Templates

Choose **Templates** under **Configure** in the management UI. Summary cards show total, generic, captcha, waiting-room, and sign-in counts. The compact template table shows name and description, kind and content type, usage count, required placeholders, updated time, and actions.

1. Choose **Add Template**.
2. Select the template kind.
3. Set a name, description, content type, and body.
4. Use the HTML editor for page templates. It includes HTML autocomplete, Emmet expansion with `Tab`, placeholder autocomplete, and a live preview with sample values.
5. Save the template, then select it from the relevant rate-limit, WAF rule, or local identity-provider drawer. The API supports selecting a generic template for a static route target, but the current route drawer exposes inline static bodies only; configure that association through the management API.

The **New Generic Body**, **New Captcha Page**, **New Waiting Room**, and **New Sign-in Page** shortcuts below the table open the same drawer with the corresponding kind preselected.

Template names follow the same public resource name rules as listeners, routes, targets, and rules: 1 to 64 characters, starting with an alphanumeric character, using only letters, numbers, dots, dashes, and underscores.

Template kind is immutable after creation, so the edit drawer locks **Kind**. To change a generic body into a WAF page template, create a new template of the target kind and move references to it.

<figure class="doc-screenshot">
  <img src="../assets/new/response_template_page.png" alt="p2pstream Response Templates page showing kind summary cards and a compact table with template descriptions, kind, content type, usage count, required placeholders, updated time, and actions">
  <figcaption>The Templates page shows reusable response bodies and WAF pages with their kind, content type, usage count, required placeholders, and whether deletion is available.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/edit_template_modal.png" alt="p2pstream response template drawer showing name, locked kind, content type, description, body editor, and preview controls">
  <figcaption>The template drawer stores generic bodies and full WAF page templates separately so each caller can enforce the right validation and runtime behavior.</figcaption>
</figure>

## Placeholders

Generic templates are raw bodies. Placeholder text such as <code v-pre>{{ .host }}</code> remains literal in generic templates.

WAF page templates support these placeholders:

| Placeholder | Required for | Description |
| --- | --- | --- |
| <code v-pre>{{ .captcha_element_html }}</code> | WAF captcha page | Server-generated captcha widget, hidden fields, and submit form. This value is trusted server HTML. |
| <code v-pre>{{ .queue_position }}</code> | WAF waiting room page | Visitor's current queue position. |
| <code v-pre>{{ .retry_after_seconds }}</code> | WAF waiting room page | Seconds until the browser should check admission again. |
| <code v-pre>{{ .host }}</code> | Optional | Request host shown to the visitor. |
| <code v-pre>{{ .rule_name }}</code> | Optional | Name of the WAF rule rendering the page. |
| <code v-pre>{{ .reference_id }}</code> | Optional | Short reference ID for support and logs. |
| <code v-pre>{{ .page_title }}</code> | Optional | Configured page title. |
| <code v-pre>{{ .page_body }}</code> | Optional | Configured page body copy. |
| <code v-pre>{{ .status_url }}</code> | Optional | Captcha verification endpoint or waiting-room status endpoint. |

Normal placeholder values are escaped by `html/template`. Only `captcha_element_html` is inserted as trusted HTML, because it is generated by the server.

Local access sign-in templates support these placeholders:

| Placeholder | Required | Description |
| --- | --- | --- |
| <code v-pre>{{ .login_action }}</code> | Yes | Same-origin form action that preserves the visitor's requested query parameters. |
| <code v-pre>{{ .csrf_field_name }}</code> | Yes | Server-defined name for the hidden CSRF input. |
| <code v-pre>{{ .csrf_token }}</code> | Yes | Short-lived CSRF token placed in that hidden input. |
| <code v-pre>{{ .username_field_name }}</code> | Yes | Server-defined username input name. |
| <code v-pre>{{ .password_field_name }}</code> | Yes | Server-defined password input name. |
| <code v-pre>{{ .username }}</code> | No | Normalized submitted username, retained after an unsuccessful sign-in. |
| <code v-pre>{{ .error_message }}</code> | No | Empty on the initial page; contains safe server error copy after a failed, expired, or throttled sign-in. |
| <code v-pre>{{ .provider_name }}</code> | No | Selected local provider's management name. |
| <code v-pre>{{ .page_title }}</code> | No | Selected local provider's management name for use in the document title. |
| <code v-pre>{{ .host }}</code> | No | Request host shown to the visitor. |

All local sign-in values are untrusted strings and are context-escaped by `html/template`; none is inserted as trusted HTML. A local provider uses the same selected template for the first sign-in form and its authentication-error states. HTTP Basic challenges remain plain-text protocol responses and do not render a page.

<figure class="doc-screenshot">
  <img src="../assets/new/edit_template_modal_with_dynamic_values_waf.png" alt="p2pstream WAF response template drawer showing allowed dynamic placeholders and a rendered sample preview">
  <figcaption>WAF page templates include placeholder assistance and sample rendering so required captcha or waiting-room values are present before the template can be selected by a WAF rule.</figcaption>
</figure>

## Validation Rules

On create or update, p2pstream:

- requires valid UTF-8 bodies,
- limits generic bodies to 64 KiB,
- limits HTML page templates to 128 KiB,
- parses HTML page templates with `html/template`,
- rejects placeholders that are not allowed for the selected page kind,
- rejects missing required placeholders,
- requires page templates to use an HTML content type,
- executes page templates with sample data to catch render errors.

Reference validation is kind-aware:

- Static targets, rate-limit rules, and WAF block responses can only select `generic_body` templates.
- Captcha WAF rules can only select `waf_captcha_page` templates for captcha pages.
- Waiting-room WAF rules can only select `waf_waiting_room_page` templates for waiting-room pages.
- Local access providers can only select `local_access_login_page` templates for sign-in pages.
- Captcha page templates cannot be selected for non-captcha WAF rules.
- Waiting-room page templates cannot be selected for non-waiting-room WAF rules.

Delete a template only after removing all references to it. The UI shows usage counts and disables deletion while the template is still referenced.

## Runtime Effects

Generic body templates are resolved when the public proxy snapshot is built. If a referenced template is missing or has the wrong kind, the configuration is rejected until the reference is fixed.

WAF captcha and waiting-room page templates are rendered for each matching decision. Rendered WAF pages are served as HTML with `Cache-Control: no-store`. If a page template unexpectedly fails to render at runtime, p2pstream falls back to the built-in WAF interstitial page.

Local sign-in templates are compiled when the public proxy snapshot is built and rendered for each form response. They use `Cache-Control: no-store`, a no-script content security policy, `form-action 'self'`, frame denial, and no-referrer handling. A missing, invalid, or wrong-kind selected template makes the affected access-provider configuration fail closed. The seeded `local-access-login-default` template is automatically selected for existing local providers during migration and for new providers when the API omits a template ID.

## Examples

Generic maintenance body:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Maintenance</title>
</head>
<body>
  <main>
    <h1>Maintenance in progress</h1>
    <p>Please try again soon.</p>
  </main>
</body>
</html>
```

Captcha page template:

::: v-pre

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>{{ .host }} security check</h1>
    <p>{{ .page_body }}</p>
    {{ .captcha_element_html }}
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
```

:::

Waiting-room page template:

::: v-pre

```html
<!doctype html>
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
    <p>Queue position: {{ .queue_position }}</p>
    <p>Next check: {{ .retry_after_seconds }} seconds</p>
    <a href="{{ .status_url }}">Check status</a>
    <footer>Reference ID: {{ .reference_id }}</footer>
  </main>
</body>
</html>
```

:::

Local sign-in page template:

::: v-pre

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sign in · {{ .page_title }}</title>
</head>
<body>
  <main>
    <h1>Sign in to {{ .provider_name }}</h1>
    <p role="alert">{{ .error_message }}</p>
    <form method="post" action="{{ .login_action }}">
      <input type="hidden" name="{{ .csrf_field_name }}" value="{{ .csrf_token }}">
      <label>Username <input name="{{ .username_field_name }}" value="{{ .username }}" autocomplete="username" required></label>
      <label>Password <input type="password" name="{{ .password_field_name }}" autocomplete="current-password" required></label>
      <button type="submit">Continue</button>
    </form>
  </main>
</body>
</html>
```

:::

## Related Tasks

- [Redirects and static responses](../guides/redirects-and-static-responses)
- [Rate limits reference](./rate-limits)
- [WAF reference](./waf)
- [Identity-aware access](./access-control)
