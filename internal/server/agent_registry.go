package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	agentTokenBytes           = 32
	agentPublicIDBytes        = 16
	agentPublicIDPrefix       = "agent-"
	agentPublicIDMaxAttempts  = 5
	agentPublicIDEncodedBytes = 26
	reservedAgentLabelPrefix  = "p2pstream.io/"
	agentIDSystemLabelKey     = "p2pstream.io/agent-id"
)

var newAgentPublicID = randomAgentPublicID

func (a *App) CreateAgent(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreateAgentRequest],
) (*connect.Response[p2pstreamv1.CreateAgentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	name, err := validateAgentName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	labels, err := validateAgentUserLabels(req.Msg.Labels)
	if err != nil {
		return nil, err
	}
	token, tokenHash, err := newAgentToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	agent, err := a.createAgentWithGeneratedPublicIDTx(ctx, qtx, name, tokenHash, boolInt(req.Msg.Enabled))
	if err != nil {
		return nil, err
	}
	if err := ensureAgentSystemLabelTx(ctx, qtx, agent); err != nil {
		return nil, err
	}
	if err := replaceAgentUserLabelsTx(ctx, qtx, agent.ID, labels); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreateAgentResponse{
		Agent: a.agentToProto(ctx, agent),
		Token: token,
	}), nil
}

func (a *App) UpdateAgent(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdateAgentRequest],
) (*connect.Response[p2pstreamv1.UpdateAgentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	name, err := validateAgentName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	labels, err := validateAgentUserLabels(req.Msg.Labels)
	if err != nil {
		return nil, err
	}
	existing, err := a.DB.GetAgent(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if !req.Msg.Enabled {
		if err := a.ensureAgentCanBeDisabled(ctx, req.Msg.Id); err != nil {
			return nil, err
		}
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	agent, err := qtx.UpdateAgent(ctx, db.UpdateAgentParams{
		ID:      req.Msg.Id,
		Name:    name,
		Enabled: boolInt(req.Msg.Enabled),
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := ensureAgentSystemLabelTx(ctx, qtx, existing); err != nil {
		return nil, err
	}
	if err := replaceAgentUserLabelsTx(ctx, qtx, agent.ID, labels); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgent(req.Msg.Id)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdateAgentResponse{Agent: a.agentToProto(ctx, agent)}), nil
}

func (a *App) DeleteAgent(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeleteAgentRequest],
) (*connect.Response[p2pstreamv1.DeleteAgentResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.AgentHub.connectedByID(req.Msg.Id) != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("connected agent cannot be deleted"))
	}
	if err := a.ensureAgentCanBeDisabled(ctx, req.Msg.Id); err != nil {
		return nil, err
	}
	if err := a.DB.DeleteAgent(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgent(req.Msg.Id)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeleteAgentResponse{}), nil
}

func (a *App) RotateAgentToken(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.RotateAgentTokenRequest],
) (*connect.Response[p2pstreamv1.RotateAgentTokenResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	token, tokenHash, err := newAgentToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agent, err := a.DB.UpdateAgentToken(ctx, db.UpdateAgentTokenParams{
		ID:        req.Msg.Id,
		TokenHash: tokenHash,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	a.revokeAgentConnection(agent.ID)
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgent(agent.ID)
	}
	return connect.NewResponse(&p2pstreamv1.RotateAgentTokenResponse{
		Agent: a.agentToProto(ctx, agent),
		Token: token,
	}), nil
}

func (a *App) authenticateAgent(ctx context.Context, publicID string, authorization string) (db.Agent, error) {
	if a.DB == nil {
		return db.Agent{}, errors.New("agent registry requires a database")
	}
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return db.Agent{}, errors.New("agent id required")
	}
	token := bearerToken(authorization)
	if token == "" {
		return db.Agent{}, errors.New("agent token required")
	}
	agent, err := a.DB.GetAgentByPublicID(ctx, publicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Agent{}, errors.New("agent is not registered")
		}
		return db.Agent{}, err
	}
	if agent.Enabled == 0 {
		return db.Agent{}, errors.New("agent is disabled")
	}
	if subtle.ConstantTimeCompare([]byte(hashAgentToken(token)), []byte(agent.TokenHash)) != 1 {
		return db.Agent{}, errors.New("invalid agent token")
	}
	if err := a.requireAgentClientCertificate(ctx, publicID); err != nil {
		return db.Agent{}, err
	}
	return agent, nil
}

func (a *App) ensureBootstrapAgent(ctx context.Context) {
	if a.Config == nil || a.DB == nil {
		return
	}
	publicID := strings.TrimSpace(a.Config.BootstrapAgentID)
	name := strings.TrimSpace(a.Config.BootstrapAgentName)
	token := strings.TrimSpace(a.Config.BootstrapAgentToken)
	if publicID == "" && name == "" && token == "" {
		return
	}
	if publicID == "" || name == "" || token == "" {
		log.Warn().Msg("BOOTSTRAP_AGENT_ID, BOOTSTRAP_AGENT_NAME, and BOOTSTRAP_AGENT_TOKEN must all be set to bootstrap an agent")
		return
	}
	publicID, name, err := validateAgentIdentity(publicID, name)
	if err != nil {
		log.Warn().Err(err).Msg("Bootstrap agent configuration is invalid")
		return
	}
	agent, err := a.DB.UpsertBootstrapAgent(ctx, db.UpsertBootstrapAgentParams{
		PublicID:  publicID,
		Name:      name,
		TokenHash: hashAgentToken(token),
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to upsert bootstrap agent")
		return
	}
	if err := a.ensureAgentSystemLabel(ctx, agent); err != nil {
		log.Warn().Err(err).Msg("Failed to upsert bootstrap agent system label")
	}
}

func (a *App) ensureAgentCanBeDisabled(ctx context.Context, agentID int64) error {
	return nil
}

func (a *App) createAgentWithGeneratedPublicID(ctx context.Context, name string, tokenHash string, enabled int64) (db.Agent, error) {
	return a.createAgentWithGeneratedPublicIDTx(ctx, a.DB.Queries, name, tokenHash, enabled)
}

func (a *App) createAgentWithGeneratedPublicIDTx(ctx context.Context, q *db.Queries, name string, tokenHash string, enabled int64) (db.Agent, error) {
	for attempt := 0; attempt < agentPublicIDMaxAttempts; attempt++ {
		publicID, err := newAgentPublicID()
		if err != nil {
			return db.Agent{}, connect.NewError(connect.CodeInternal, err)
		}
		if _, err := validateGeneratedAgentPublicID(publicID); err != nil {
			return db.Agent{}, connect.NewError(connect.CodeInternal, errors.New("generated invalid agent id"))
		}
		agent, err := q.CreateAgent(ctx, db.CreateAgentParams{
			PublicID:  publicID,
			Name:      name,
			TokenHash: tokenHash,
			Enabled:   enabled,
		})
		if err == nil {
			return agent, nil
		}
		if isUniqueConstraintError(err) {
			continue
		}
		return db.Agent{}, publicDBError(err)
	}
	return db.Agent{}, connect.NewError(connect.CodeInternal, errors.New("failed to generate unique agent id"))
}

func validateAgentIdentity(publicID string, name string) (string, string, error) {
	publicID, err := validateAgentPublicID(publicID)
	if err != nil {
		return "", "", err
	}
	name, err = validateAgentName(name)
	if err != nil {
		return "", "", err
	}
	return publicID, name, nil
}

func validateAgentPublicID(publicID string) (string, error) {
	publicID = strings.TrimSpace(publicID)
	if !publicNamePattern.MatchString(publicID) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent id must be 1-64 letters, numbers, dots, underscores, or hyphens and start with a letter or number"))
	}
	return publicID, nil
}

func validateGeneratedAgentPublicID(publicID string) (string, error) {
	publicID, err := validateAgentPublicID(publicID)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(publicID, agentPublicIDPrefix) || len(publicID) != len(agentPublicIDPrefix)+agentPublicIDEncodedBytes {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("generated agent id is invalid"))
	}
	for _, ch := range strings.TrimPrefix(publicID, agentPublicIDPrefix) {
		if (ch < 'a' || ch > 'z') && (ch < '2' || ch > '7') {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("generated agent id is invalid"))
		}
	}
	return publicID, nil
}

func validateAgentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent name must be 1-128 characters"))
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent name must not contain CR or LF"))
	}
	return name, nil
}

func validateAgentUserLabels(labels map[string]string) (map[string]string, error) {
	if len(labels) == 0 {
		return map[string]string{}, nil
	}
	resp := make(map[string]string, len(labels))
	for key, value := range labels {
		key, value, err := validateAgentLabel(key, value)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(key, reservedAgentLabelPrefix) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent labels under p2pstream.io/ are system-owned"))
		}
		if _, exists := resp[key]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate agent label key after normalization"))
		}
		resp[key] = value
	}
	return resp, nil
}

func validateAgentLabel(key string, value string) (string, string, error) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || len(key) > 128 {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent label keys must be 1-128 characters"))
	}
	if len(value) > 256 {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent label values must be at most 256 characters"))
	}
	if strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent labels must not contain CR or LF"))
	}
	return key, value, nil
}

func (a *App) ensureAgentSystemLabel(ctx context.Context, agent db.Agent) error {
	return ensureAgentSystemLabelTx(ctx, a.DB.Queries, agent)
}

func ensureAgentSystemLabelTx(ctx context.Context, q *db.Queries, agent db.Agent) error {
	if _, err := q.UpsertAgentLabel(ctx, db.UpsertAgentLabelParams{
		AgentID: agent.ID,
		Key:     agentIDSystemLabelKey,
		Value:   agent.PublicID,
		Source:  "system",
	}); err != nil {
		return publicDBError(err)
	}
	return nil
}

func (a *App) replaceAgentUserLabels(ctx context.Context, agentID int64, labels map[string]string) error {
	return replaceAgentUserLabelsTx(ctx, a.DB.Queries, agentID, labels)
}

func replaceAgentUserLabelsTx(ctx context.Context, q *db.Queries, agentID int64, labels map[string]string) error {
	if err := q.DeleteUserAgentLabelsByAgent(ctx, agentID); err != nil {
		return publicDBError(err)
	}
	for key, value := range labels {
		if _, err := q.UpsertAgentLabel(ctx, db.UpsertAgentLabelParams{
			AgentID: agentID,
			Key:     key,
			Value:   value,
			Source:  "user",
		}); err != nil {
			return publicDBError(err)
		}
	}
	return nil
}

func newAgentToken() (token string, tokenHash string, err error) {
	buf := make([]byte, agentTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashAgentToken(token), nil
}

func randomAgentPublicID() (string, error) {
	buf := make([]byte, agentPublicIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return agentPublicIDPrefix + strings.ToLower(encoded), nil
}

func hashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func (a *App) agentToProto(ctx context.Context, agent db.Agent) *p2pstreamv1.Agent {
	return a.agentToProtoWithLatestStats(ctx, agent, true)
}

func (a *App) agentToProtoWithLatestStats(ctx context.Context, agent db.Agent, useDBFallback bool) *p2pstreamv1.Agent {
	conn := a.AgentHub.connectedByID(agent.ID)
	resp := &p2pstreamv1.Agent{
		Id:                           agent.ID,
		PublicId:                     agent.PublicID,
		Name:                         agent.Name,
		Enabled:                      agent.Enabled != 0,
		Connected:                    conn != nil,
		CreatedAtUnixMillis:          agent.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:          agent.UpdatedAt.UnixMilli(),
		LastConnectedAtUnixMillis:    nullTimeUnixMillis(agent.LastConnectedAt),
		LastDisconnectedAtUnixMillis: nullTimeUnixMillis(agent.LastDisconnectedAt),
		Labels:                       map[string]string{},
	}
	if a.DB != nil {
		labels, err := a.DB.ListAgentLabelsByAgent(ctx, agent.ID)
		if err == nil {
			for _, label := range labels {
				resp.Labels[label.Key] = label.Value
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Debug().Err(err).Int64("agent_id", agent.ID).Msg("Failed to load agent labels")
		}
	}
	if conn != nil {
		resp.ActiveRequests = conn.ActiveRequests.Load()
	}
	if latest, ok := a.latestAgentStatsSnapshot(agent.ID); ok {
		resp.LatestStats = latest
	} else if useDBFallback && a.DB != nil {
		latest, err := a.DB.GetLatestAgentStatByAgent(ctx, sql.NullInt64{Int64: agent.ID, Valid: true})
		if err == nil {
			resp.LatestStats = agentStatRowToProto(latest)
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Debug().Err(err).Int64("agent_id", agent.ID).Msg("Failed to load latest agent stat")
		}
	}
	return resp
}

func agentStatRowToProto(row db.AgentStat) *p2pstreamv1.AgentStatsSnapshot {
	return &p2pstreamv1.AgentStatsSnapshot{
		MemorySysMb:          row.MemoryMb,
		NumGoroutine:         row.Goroutines,
		ReqSuccess:           row.ReqSuccess,
		ReqClientError:       row.ReqClientError,
		ReqServerError:       row.ReqServerError,
		ReqInternalError:     row.ReqInternalError,
		BytesReceived:        uint64(row.BytesRx),
		BytesSent:            uint64(row.BytesTx),
		CpuPercent:           row.CpuPercent,
		ReportedAtUnixMillis: row.ReportedAt.UnixMilli(),
	}
}

func nullTimeUnixMillis(value sql.NullTime) int64 {
	if !value.Valid || value.Time.IsZero() {
		return 0
	}
	return value.Time.UnixMilli()
}
