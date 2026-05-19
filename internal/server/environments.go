package server

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/db"
)

const (
	environmentTransportDirect = "direct"
	environmentTransportAgent  = "agent"

	minEnvironmentResponseHeaderTimeoutMillis     = int64(1000)
	defaultEnvironmentResponseHeaderTimeoutMillis = int64(10000)
	maxEnvironmentResponseHeaderTimeoutMillis     = int64(300000)
)

var defaultEnvironmentResponseHeaderTimeout = time.Duration(defaultEnvironmentResponseHeaderTimeoutMillis) * time.Millisecond

type environmentMutationInput struct {
	ID                          int64
	Name                        string
	ManagementURL               string
	Transport                   string
	AgentID                     sql.NullInt64
	AccessToken                 string
	ResponseHeaderTimeoutMillis int64
	Enabled                     int64
}

func (a *App) ListEnvironments(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.ListEnvironmentsRequest],
) (*connect.Response[p2pstreamv1.ListEnvironmentsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	rows, err := a.DB.ListEnvironments(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &p2pstreamv1.ListEnvironmentsResponse{Environments: make([]*p2pstreamv1.Environment, 0, len(rows))}
	for _, row := range rows {
		resp.Environments = append(resp.Environments, a.environmentToProto(ctx, row))
	}
	return connect.NewResponse(resp), nil
}

func (a *App) CreateEnvironment(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreateEnvironmentRequest],
) (*connect.Response[p2pstreamv1.CreateEnvironmentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	input, err := a.validateEnvironmentInput(ctx, req.Msg.Name, req.Msg.ManagementUrl, req.Msg.Transport, req.Msg.AgentId, req.Msg.AccessToken, req.Msg.ResponseHeaderTimeoutMillis, req.Msg.Enabled, nil)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreateEnvironment(ctx, db.CreateEnvironmentParams{
		Name:                        input.Name,
		ManagementUrl:               input.ManagementURL,
		Transport:                   input.Transport,
		AgentID:                     input.AgentID,
		AccessToken:                 input.AccessToken,
		ResponseHeaderTimeoutMillis: input.ResponseHeaderTimeoutMillis,
		Enabled:                     input.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.CreateEnvironmentResponse{Environment: a.environmentToProto(ctx, row)}), nil
}

func (a *App) UpdateEnvironment(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdateEnvironmentRequest],
) (*connect.Response[p2pstreamv1.UpdateEnvironmentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetEnvironment(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	input, err := a.validateEnvironmentInput(ctx, req.Msg.Name, req.Msg.ManagementUrl, req.Msg.Transport, req.Msg.AgentId, req.Msg.AccessToken, req.Msg.ResponseHeaderTimeoutMillis, req.Msg.Enabled, &existing)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdateEnvironment(ctx, db.UpdateEnvironmentParams{
		ID:                          input.ID,
		Name:                        input.Name,
		ManagementUrl:               input.ManagementURL,
		Transport:                   input.Transport,
		AgentID:                     input.AgentID,
		AccessToken:                 input.AccessToken,
		ResponseHeaderTimeoutMillis: input.ResponseHeaderTimeoutMillis,
		Enabled:                     input.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if row.ManagementUrl != existing.ManagementUrl {
		row, err = a.DB.ClearEnvironmentTrust(ctx, row.ID)
		if err != nil {
			return nil, publicDBError(err)
		}
	}
	return connect.NewResponse(&p2pstreamv1.UpdateEnvironmentResponse{Environment: a.environmentToProto(ctx, row)}), nil
}

func (a *App) DeleteEnvironment(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeleteEnvironmentRequest],
) (*connect.Response[p2pstreamv1.DeleteEnvironmentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeleteEnvironment(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.DeleteEnvironmentResponse{}), nil
}

func (a *App) DiscoverEnvironmentCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DiscoverEnvironmentCertificateRequest],
) (*connect.Response[p2pstreamv1.DiscoverEnvironmentCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	row, err := a.DB.GetEnvironment(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	cert, fingerprint, err := a.discoverEnvironmentCertificate(ctx, row)
	if err != nil {
		updated, updateErr := a.DB.UpdateEnvironmentCheckResult(ctx, db.UpdateEnvironmentCheckResultParams{
			LastError: err.Error(),
			ID:        row.ID,
		})
		if updateErr == nil {
			row = updated
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	updated, err := a.DB.UpdateEnvironmentObservedCertificate(ctx, db.UpdateEnvironmentObservedCertificateParams{
		LastObservedCertificatePem:    environmentCertificatePEM(cert),
		LastObservedCertificateSha256: fingerprint,
		LastError:                     "",
		ID:                            row.ID,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.DiscoverEnvironmentCertificateResponse{
		Environment: a.environmentToProto(ctx, updated),
		Certificate: environmentCertificateProto(cert),
	}), nil
}

func (a *App) TrustEnvironmentCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.TrustEnvironmentCertificateRequest],
) (*connect.Response[p2pstreamv1.TrustEnvironmentCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	row, err := a.DB.GetEnvironment(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if strings.TrimSpace(row.LastObservedCertificatePem) == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("discover the environment certificate before trusting it"))
	}
	if normalizeEnvironmentCertificateFingerprint(row.LastObservedCertificateSha256) != normalizeEnvironmentCertificateFingerprint(req.Msg.Sha256Fingerprint) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("observed certificate fingerprint does not match"))
	}
	cert, err := parseEnvironmentCertificatePEM(row.LastObservedCertificatePem)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err := verifyEnvironmentCertificateForURL(cert, row.ManagementUrl); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	updated, err := a.DB.TrustEnvironmentCertificate(ctx, db.TrustEnvironmentCertificateParams{
		TrustedCertificateSubject:  cert.Subject.String(),
		TrustedCertificateNotAfter: sql.NullTime{Time: cert.NotAfter, Valid: true},
		ID:                         row.ID,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.TrustEnvironmentCertificateResponse{Environment: a.environmentToProto(ctx, updated)}), nil
}

func (a *App) TestEnvironment(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.TestEnvironmentRequest],
) (*connect.Response[p2pstreamv1.TestEnvironmentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	row, err := a.DB.GetEnvironment(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	client, err := a.environmentManagementClient(row)
	if err != nil {
		updated, updateErr := a.DB.UpdateEnvironmentCheckResult(ctx, db.UpdateEnvironmentCheckResultParams{LastError: err.Error(), ID: row.ID})
		if updateErr == nil {
			row = updated
		}
		return nil, err
	}
	status, err := client.GetStatus(ctx, connect.NewRequest(&p2pstreamv1.GetStatusRequest{}))
	if err != nil {
		updated, updateErr := a.DB.UpdateEnvironmentCheckResult(ctx, db.UpdateEnvironmentCheckResultParams{LastError: err.Error(), ID: row.ID})
		if updateErr == nil {
			row = updated
		}
		return nil, err
	}
	row, err = a.DB.UpdateEnvironmentCheckResult(ctx, db.UpdateEnvironmentCheckResultParams{LastError: "", ID: row.ID})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.TestEnvironmentResponse{
		Environment: a.environmentToProto(ctx, row),
		Status:      status.Msg,
	}), nil
}

func (a *App) validateEnvironmentInput(ctx context.Context, name string, rawURL string, transport p2pstreamv1.EnvironmentTransport, agentID int64, accessToken string, timeoutMillis int64, enabled bool, existing *db.Environment) (environmentMutationInput, error) {
	name, err := validateAgentName(name)
	if err != nil {
		return environmentMutationInput{}, err
	}
	managementURL, err := validateEnvironmentManagementURL(rawURL)
	if err != nil {
		return environmentMutationInput{}, err
	}
	transportValue, err := validateEnvironmentTransport(transport)
	if err != nil {
		return environmentMutationInput{}, err
	}
	agentRef := sql.NullInt64{}
	if transportValue == environmentTransportAgent {
		if agentID <= 0 {
			return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("agent transport requires a local agent"))
		}
		if _, err := a.DB.GetAgent(ctx, agentID); err != nil {
			return environmentMutationInput{}, publicDBError(err)
		}
		agentRef = sql.NullInt64{Int64: agentID, Valid: true}
	} else if agentID != 0 {
		return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("direct transport cannot select an agent"))
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" && existing != nil {
		accessToken = existing.AccessToken
	}
	if accessToken == "" {
		return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("environment access token is required"))
	}
	if !strings.HasPrefix(accessToken, managementAccessTokenPrefix) {
		return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("environment access token must be a p2pstream management access token"))
	}
	if strings.ContainsAny(accessToken, "\r\n") {
		return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("environment access token must not contain CR or LF"))
	}
	if timeoutMillis == 0 {
		timeoutMillis = defaultEnvironmentResponseHeaderTimeoutMillis
	}
	if timeoutMillis < minEnvironmentResponseHeaderTimeoutMillis || timeoutMillis > maxEnvironmentResponseHeaderTimeoutMillis {
		return environmentMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("environment response header timeout must be between 1000 and 300000 milliseconds"))
	}
	return environmentMutationInput{
		ID:                          existingEnvironmentID(existing),
		Name:                        name,
		ManagementURL:               managementURL,
		Transport:                   transportValue,
		AgentID:                     agentRef,
		AccessToken:                 accessToken,
		ResponseHeaderTimeoutMillis: timeoutMillis,
		Enabled:                     boolInt(enabled),
	}, nil
}

func existingEnvironmentID(existing *db.Environment) int64 {
	if existing == nil {
		return 0
	}
	return existing.ID
}

func validateEnvironmentManagementURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("environment management URL must be an absolute HTTPS URL"))
	}
	if parsed.Scheme != "https" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("environment management URL must use HTTPS"))
	}
	if parsed.Fragment != "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("environment management URL must not include a fragment"))
	}
	if parsed.RawQuery != "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("environment management URL must not include a query string"))
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validateEnvironmentTransport(transport p2pstreamv1.EnvironmentTransport) (string, error) {
	switch transport {
	case p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_UNSPECIFIED, p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT:
		return environmentTransportDirect, nil
	case p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_AGENT:
		return environmentTransportAgent, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported environment transport"))
	}
}

func (a *App) discoverEnvironmentCertificate(ctx context.Context, row db.Environment) (*x509.Certificate, string, error) {
	timeout := time.Duration(row.ResponseHeaderTimeoutMillis) * time.Millisecond
	if row.Transport == environmentTransportAgent {
		return a.discoverEnvironmentCertificateViaAgent(ctx, row, timeout)
	}
	return discoverCertificateDirect(ctx, row.ManagementUrl, timeout)
}

func (a *App) environmentToProto(ctx context.Context, row db.Environment) *p2pstreamv1.Environment {
	resp := &p2pstreamv1.Environment{
		Id:                          row.ID,
		Name:                        row.Name,
		ManagementUrl:               row.ManagementUrl,
		Transport:                   protoEnvironmentTransport(row.Transport),
		AgentId:                     row.AgentID.Int64,
		Enabled:                     row.Enabled != 0,
		AccessTokenConfigured:       row.AccessToken != "",
		TrustState:                  environmentTrustState(row),
		TrustedCertificate:          environmentCertificateProtoFromPEM(row.TrustedCertificatePem),
		ObservedCertificate:         environmentCertificateProtoFromPEM(row.LastObservedCertificatePem),
		LastError:                   row.LastError,
		LastCheckedAtUnixMillis:     nullTimeUnixMillis(row.LastCheckedAt),
		CreatedAtUnixMillis:         row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:         row.UpdatedAt.UnixMilli(),
		ResponseHeaderTimeoutMillis: row.ResponseHeaderTimeoutMillis,
	}
	if row.AgentID.Valid && a != nil {
		if agent, err := a.DB.GetAgent(ctx, row.AgentID.Int64); err == nil {
			resp.AgentName = agent.Name
		}
		if a.AgentHub != nil && a.AgentHub.connectedByID(row.AgentID.Int64) != nil {
			resp.AgentConnected = true
		}
	}
	return resp
}

func protoEnvironmentTransport(value string) p2pstreamv1.EnvironmentTransport {
	switch value {
	case environmentTransportAgent:
		return p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_AGENT
	default:
		return p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT
	}
}

func environmentTrustState(row db.Environment) p2pstreamv1.EnvironmentTrustState {
	if strings.TrimSpace(row.TrustedCertificatePem) == "" {
		return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_UNTRUSTED
	}
	trusted, err := parseEnvironmentCertificatePEM(row.TrustedCertificatePem)
	if err != nil {
		return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_CHANGED
	}
	now := time.Now()
	if now.After(trusted.NotAfter) {
		return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_EXPIRED
	}
	if strings.TrimSpace(row.LastObservedCertificatePem) != "" {
		observed, err := parseEnvironmentCertificatePEM(row.LastObservedCertificatePem)
		if err != nil {
			return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_CHANGED
		}
		if now.After(observed.NotAfter) {
			return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_EXPIRED
		}
		if normalizeEnvironmentCertificateFingerprint(certificateSHA256Fingerprint(observed)) != normalizeEnvironmentCertificateFingerprint(row.TrustedCertificateSha256) {
			return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_CHANGED
		}
	}
	return p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_TRUSTED
}

func ensureEnvironmentTrusted(row db.Environment) error {
	switch environmentTrustState(row) {
	case p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_TRUSTED:
		return nil
	case p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_EXPIRED:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("environment certificate is expired"))
	case p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_CHANGED:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("environment certificate changed; trust the new certificate before continuing"))
	default:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("environment certificate is not trusted"))
	}
}

func (a *App) environmentManagementClient(row db.Environment) (p2pstreamv1connect.AgentManagementServiceClient, error) {
	httpClient, err := a.environmentHTTPClient(row)
	if err != nil {
		return nil, err
	}
	return p2pstreamv1connect.NewAgentManagementServiceClient(httpClient, row.ManagementUrl), nil
}
