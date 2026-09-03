package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"
	"golang.org/x/mod/semver"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

const (
	agentUpdaterEnrollmentTokenBytes  = 32
	agentUpdaterEnrollmentTokenTTL    = 10 * time.Minute
	agentUpdaterEnrollmentTokenMinTTL = time.Minute
	agentUpdaterEnrollmentTokenMaxTTL = time.Hour
	agentUpdaterEnrollmentReceiptTTL  = 24 * time.Hour
	agentUpdaterBootstrapClockSkew    = 5 * time.Minute
	agentUpdateEventLimit             = 100
	agentUpdateMaxAgents              = 1000
	agentUpdateMaxArtifacts           = 32
	agentUpdateMaxString              = 256
	agentUpdateMaxFailureDetail       = 2048
	agentUpdatePollInterval           = 30 * time.Second
	agentUpdateWorkerFreshness        = 2 * time.Minute
	agentUpdateDrainTimeout           = 5 * time.Minute
	agentUpdatePostActionTimeout      = 5 * time.Minute
	agentUpdateStageTimeout           = 30 * time.Minute
	agentUpdateMaintenanceInterval    = 10 * time.Second
	agentUpdateEnrollMaxRequestBytes  = int64(32 << 10)
	agentUpdateCheckMaxRequestBytes   = int64(4 << 10)
	agentUpdateReportMaxRequestBytes  = int64(256 << 10)
)

type agentUpdateAdmissionContextKey struct{}

var (
	agentUpdateDigestRE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	agentUpdateTokenRE        = regexp.MustCompile(`^p2puet_[A-Za-z0-9_-]{43}$`)
	agentUpdatePlatformRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	agentUpdateArtifactNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	agentUpdateRepositoryRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
)

var errAgentUpdateCordoned = errors.New("agent is cordoned for managed update")

// TrustedAgentUpdateCatalog is the only authority for campaign targets. A
// management request may select a digest, but it cannot introduce metadata.
// Implementations must return fresh clones or otherwise immutable messages.
type TrustedAgentUpdateCatalog interface {
	ListTrustedAgentUpdateTargets(context.Context) ([]*p2pstreamv1.AgentUpdateTarget, error)
	ResolveTrustedAgentUpdateTarget(context.Context, string) (*p2pstreamv1.AgentUpdateTarget, error)
}

// AgentUpdateBootstrapProvider supplies only pinned trust/bootstrap material;
// it never supplies commands, local paths, or mutable artifact URLs.
type AgentUpdateBootstrapProvider interface {
	AgentUpdateBootstrapConfig(context.Context) (trustedRootMetadataBase64 string, pinnedRepository string, err error)
}

type agentUpdateBootstrap struct {
	RootMetadataBase64 string
	RootSHA256         string
	RootVersion        int64
	PinnedRepository   string
}

type agentUpdateCampaignRow struct {
	db.AgentUpdateCampaign
	Artifacts []*p2pstreamv1.AgentUpdateArtifact
}

type agentUpdateAssignmentRow struct {
	db.AgentUpdateAssignment
	AgentPublicID string
	AgentName     string
}

type agentUpdaterIdentityRow struct {
	db.AgentUpdaterIdentity
	AgentPublicID string
}

func (a *App) GenerateAgentUpdaterEnrollmentToken(ctx context.Context, req *connect.Request[p2pstreamv1.GenerateAgentUpdaterEnrollmentTokenRequest]) (*connect.Response[p2pstreamv1.GenerateAgentUpdaterEnrollmentTokenResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if req.Msg.AgentId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent_id is required"))
	}
	if _, err := a.DB.GetAgent(ctx, req.Msg.AgentId); err != nil {
		return nil, publicDBError(err)
	}
	bootstrap, err := a.loadAgentUpdateBootstrap(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if bootstrap.RootMetadataBase64 == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent update bootstrap trust is not configured"))
	}
	_, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.TtlMillis) * time.Millisecond
	if ttl == 0 {
		ttl = agentUpdaterEnrollmentTokenTTL
	}
	if ttl < agentUpdaterEnrollmentTokenMinTTL || ttl > agentUpdaterEnrollmentTokenMaxTTL {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ttl_millis must be between %d and %d", agentUpdaterEnrollmentTokenMinTTL.Milliseconds(), agentUpdaterEnrollmentTokenMaxTTL.Milliseconds()))
	}
	token, expiresAt, err := a.createAgentUpdaterEnrollmentToken(ctx, req.Msg.AgentId, ttl, bootstrap)
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.GenerateAgentUpdaterEnrollmentTokenResponse{Token: token, ExpiresAtUnixMillis: expiresAt.UnixMilli(), TrustedRootMetadataBase64: bootstrap.RootMetadataBase64, PinnedRepository: bootstrap.PinnedRepository, TrustedRootSha256: bootstrap.RootSHA256, TrustedRootVersion: bootstrap.RootVersion, ManagementAuthority: agentUpdateAuthorityProto(authorityIdentity)}), nil
}

func (a *App) loadAgentUpdateBootstrap(ctx context.Context) (agentUpdateBootstrap, error) {
	if a == nil || a.AgentUpdateBootstrap == nil {
		return agentUpdateBootstrap{}, nil
	}
	rootBase64, repository, err := a.AgentUpdateBootstrap.AgentUpdateBootstrapConfig(ctx)
	if err != nil {
		return agentUpdateBootstrap{}, err
	}
	if len(rootBase64) == 0 || len(rootBase64) > 64<<10 || len(repository) > 201 || !agentUpdateRepositoryRE.MatchString(repository) {
		return agentUpdateBootstrap{}, errors.New("agent update bootstrap provider returned invalid trust metadata or repository")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(rootBase64)
	if err != nil || len(decoded) == 0 || len(decoded) > 48<<10 || base64.StdEncoding.EncodeToString(decoded) != rootBase64 {
		return agentUpdateBootstrap{}, errors.New("agent update bootstrap provider returned non-canonical root metadata")
	}
	root, err := agentupdate.ParseRoot(decoded)
	if err != nil || root.Version == 0 || root.Version > math.MaxInt64 {
		return agentUpdateBootstrap{}, errors.New("agent update bootstrap provider returned invalid root metadata")
	}
	expiresAt, err := time.Parse(time.RFC3339, root.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC().Add(agentUpdaterEnrollmentReceiptTTL+agentUpdaterBootstrapClockSkew)) {
		return agentUpdateBootstrap{}, errors.New("agent update bootstrap root is expired or too close to expiry")
	}
	digest := sha256.Sum256(decoded)
	return agentUpdateBootstrap{RootMetadataBase64: rootBase64, RootSHA256: hex.EncodeToString(digest[:]), RootVersion: int64(root.Version), PinnedRepository: repository}, nil
}

func (a *App) createAgentUpdaterEnrollmentToken(ctx context.Context, agentID int64, ttl time.Duration, bootstrap agentUpdateBootstrap) (string, time.Time, error) {
	_, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return "", time.Time{}, err
	}
	token, tokenHash, now, expiresAt, err := newAgentUpdaterEnrollmentToken(ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	var activeAssignments int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_update_assignments WHERE agent_id=? AND (state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback'))`, agentID).Scan(&activeAssignments); err != nil {
		return "", time.Time{}, err
	}
	if activeAssignments != 0 {
		return "", time.Time{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot replace updater enrollment authority during an active assignment"))
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(enrollment_generation),0)+1 FROM (SELECT enrollment_generation FROM agent_updater_enrollment_tokens WHERE agent_id=? UNION ALL SELECT enrollment_generation FROM agent_updater_identities WHERE agent_id=?)`, agentID, agentID).Scan(&generation); err != nil {
		return "", time.Time{}, err
	}
	if generation <= 0 {
		return "", time.Time{}, errors.New("updater enrollment generation is exhausted")
	}
	// An agent has exactly one live enrollment authority. Serializing deletion
	// and insertion makes the last committed issuance the only unused token;
	// consumed receipts remain solely for same-binding lost-response recovery.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_updater_enrollment_tokens WHERE agent_id=? AND used_at IS NULL`, agentID); err != nil {
		return "", time.Time{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_updater_enrollment_tokens (agent_id,token_hash,trusted_root_sha256,trusted_root_version,pinned_repository,authority_key_id,authority_epoch,enrollment_generation,expires_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, agentID, tokenHash, bootstrap.RootSHA256, bootstrap.RootVersion, bootstrap.PinnedRepository, authorityIdentity.KeyID, int64(authorityIdentity.Epoch), generation, expiresAt, now)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func newAgentUpdaterEnrollmentToken(ttl time.Duration) (token string, tokenHash string, now time.Time, expiresAt time.Time, err error) {
	raw := make([]byte, agentUpdaterEnrollmentTokenBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", "", time.Time{}, time.Time{}, err
	}
	token = "p2puet_" + rawURLEncoding(raw)
	digest := sha256.Sum256([]byte(token))
	tokenHash = hex.EncodeToString(digest[:])
	now = time.Now().UTC().Truncate(time.Millisecond)
	expiresAt = now.Add(ttl)
	return token, tokenHash, now, expiresAt, nil
}

func rawURLEncoding(value []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(value)*8+5)/6)
	var accumulator uint32
	var bits uint
	for _, b := range value {
		accumulator = accumulator<<8 | uint32(b)
		bits += 8
		for bits >= 6 {
			bits -= 6
			out = append(out, alphabet[(accumulator>>bits)&63])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(accumulator<<(6-bits))&63])
	}
	return string(out)
}

func (a *App) EnrollAgentUpdater(ctx context.Context, req *connect.Request[p2pstreamv1.EnrollAgentUpdaterRequest]) (*connect.Response[p2pstreamv1.EnrollAgentUpdaterResponse], error) {
	release, err := a.beginAgentUpdateRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	m := req.Msg
	if !agentUpdateTokenRE.MatchString(m.Token) || strings.TrimSpace(m.AgentPublicId) == "" || len(m.AgentPublicId) > 128 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("valid token and agent_public_id are required"))
	}
	if len(m.UpdaterPublicKey) != ed25519.PublicKeySize || len(m.ActivatorPublicKey) != ed25519.PublicKeySize || bytes.Equal(m.UpdaterPublicKey, m.ActivatorPublicKey) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("distinct 32-byte updater and activator Ed25519 public keys are required"))
	}
	osName, arch := strings.TrimSpace(m.Os), strings.TrimSpace(m.Arch)
	if !agentUpdatePlatformRE.MatchString(osName) || !agentUpdatePlatformRE.MatchString(arch) || len(m.UpdaterVersion) > agentUpdateMaxString {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid updater platform or version"))
	}
	authority, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return nil, err
	}
	bootstrap, err := a.loadAgentUpdateBootstrap(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if bootstrap.RootMetadataBase64 == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent update bootstrap trust is not configured"))
	}
	digest := sha256.Sum256([]byte(m.Token))
	now := time.Now().UTC().Truncate(time.Millisecond)
	updaterKeyID := agentUpdateKeyID(m.UpdaterPublicKey)
	activatorKeyID := agentUpdateKeyID(m.ActivatorPublicKey)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	var tokenID, agentID, boundRootVersion, boundAuthorityEpoch, enrollmentGeneration int64
	var boundRootSHA256, boundRepository, boundAuthorityKeyID string
	var usedAt, receiptExpiresAt sql.NullTime
	var receiptUpdaterKeyID, receiptActivatorKeyID, receiptOS, receiptArch, receiptUpdaterVersion string
	var storedReceiptPayload, storedReceiptSignature []byte
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.agent_id,t.trusted_root_sha256,t.trusted_root_version,t.pinned_repository,t.authority_key_id,t.authority_epoch,t.enrollment_generation,t.used_at,t.receipt_expires_at,t.updater_key_id,t.activator_key_id,t.os,t.arch,t.updater_version,t.receipt_payload,t.receipt_signature FROM agent_updater_enrollment_tokens t JOIN agents a ON a.id=t.agent_id WHERE t.token_hash=? AND a.public_id=? AND a.enabled=1 AND ((t.used_at IS NULL AND t.expires_at>?) OR (t.used_at IS NOT NULL AND t.receipt_expires_at>?))`, hex.EncodeToString(digest[:]), m.AgentPublicId, now, now).Scan(&tokenID, &agentID, &boundRootSHA256, &boundRootVersion, &boundRepository, &boundAuthorityKeyID, &boundAuthorityEpoch, &enrollmentGeneration, &usedAt, &receiptExpiresAt, &receiptUpdaterKeyID, &receiptActivatorKeyID, &receiptOS, &receiptArch, &receiptUpdaterVersion, &storedReceiptPayload, &storedReceiptSignature)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater enrollment token is invalid, expired, or already used"))
	}
	if boundRootSHA256 != bootstrap.RootSHA256 || boundRootVersion != bootstrap.RootVersion || boundRepository != bootstrap.PinnedRepository || boundAuthorityKeyID != authorityIdentity.KeyID || boundAuthorityEpoch <= 0 || uint64(boundAuthorityEpoch) != authorityIdentity.Epoch || enrollmentGeneration <= 0 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater enrollment token trust binding no longer matches configured bootstrap trust"))
	}
	if usedAt.Valid {
		if receiptUpdaterKeyID != updaterKeyID || receiptActivatorKeyID != activatorKeyID || receiptOS != osName || receiptArch != arch || receiptUpdaterVersion != strings.TrimSpace(m.UpdaterVersion) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater enrollment token is already bound to a different identity"))
		}
		var storedUpdaterKey, storedActivatorKey, identityReceiptPayload, identityReceiptSignature []byte
		var enrolledAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT updater_public_key,activator_public_key,enrolled_at,enrollment_receipt_payload,enrollment_receipt_signature FROM agent_updater_identities WHERE agent_id=? AND updater_key_id=? AND activator_key_id=? AND os=? AND arch=? AND updater_version=? AND trusted_root_sha256=? AND trusted_root_version=? AND pinned_repository=? AND authority_key_id=? AND authority_epoch=? AND enrollment_generation=? AND enabled=1`, agentID, updaterKeyID, activatorKeyID, osName, arch, strings.TrimSpace(m.UpdaterVersion), boundRootSHA256, boundRootVersion, boundRepository, boundAuthorityKeyID, boundAuthorityEpoch, enrollmentGeneration).Scan(&storedUpdaterKey, &storedActivatorKey, &enrolledAt, &identityReceiptPayload, &identityReceiptSignature)
		if err != nil || !bytes.Equal(storedUpdaterKey, m.UpdaterPublicKey) || !bytes.Equal(storedActivatorKey, m.ActivatorPublicKey) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater enrollment receipt no longer matches the active identity"))
		}
		receipt := agentupdateauth.EnrollmentReceipt{
			AgentPublicID: m.AgentPublicId, UpdaterKeyID: updaterKeyID, UpdaterPublicKeySHA256: updaterKeyID,
			ActivatorKeyID: activatorKeyID, ActivatorPublicKeySHA256: activatorKeyID, OS: osName, Arch: arch,
			UpdaterVersion: strings.TrimSpace(m.UpdaterVersion), TrustedRootSHA256: boundRootSHA256,
			TrustedRootVersion: uint64(boundRootVersion), PinnedRepository: boundRepository,
			AuthorityKeyID: boundAuthorityKeyID, AuthorityEpoch: uint64(boundAuthorityEpoch),
			EnrolledAtUnixMillis: enrolledAt.UnixMilli(), ExpiresAtUnixMillis: receiptExpiresAt.Time.UnixMilli(), Generation: uint64(enrollmentGeneration),
		}
		canonical, canonicalErr := agentupdateauth.EnrollmentReceiptPayload(receipt)
		if canonicalErr != nil || !bytes.Equal(canonical, storedReceiptPayload) || !bytes.Equal(canonical, identityReceiptPayload) || !bytes.Equal(storedReceiptSignature, identityReceiptSignature) || len(storedReceiptSignature) != ed25519.SignatureSize || !ed25519.Verify(authorityIdentity.PublicKey, canonical, storedReceiptSignature) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("stored updater enrollment receipt is invalid"))
		}
		return connect.NewResponse(&p2pstreamv1.EnrollAgentUpdaterResponse{UpdaterKeyId: updaterKeyID, ActivatorKeyId: activatorKeyID, EnrolledAtUnixMillis: enrolledAt.UnixMilli(), Receipt: enrollmentReceiptProto(receipt, canonical, storedReceiptSignature)}), nil
	}
	receiptExpires := now.Add(agentUpdaterEnrollmentReceiptTTL)
	var activeAssignments int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_update_assignments WHERE agent_id=? AND (state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback'))`, agentID).Scan(&activeAssignments); err != nil {
		return nil, publicDBError(err)
	}
	if activeAssignments != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("updater identity cannot change during an active assignment"))
	}
	receipt := agentupdateauth.EnrollmentReceipt{
		AgentPublicID: m.AgentPublicId, UpdaterKeyID: updaterKeyID, UpdaterPublicKeySHA256: updaterKeyID,
		ActivatorKeyID: activatorKeyID, ActivatorPublicKeySHA256: activatorKeyID, OS: osName, Arch: arch,
		UpdaterVersion: strings.TrimSpace(m.UpdaterVersion), TrustedRootSHA256: boundRootSHA256,
		TrustedRootVersion: uint64(boundRootVersion), PinnedRepository: boundRepository,
		AuthorityKeyID: boundAuthorityKeyID, AuthorityEpoch: uint64(boundAuthorityEpoch),
		EnrolledAtUnixMillis: now.UnixMilli(), ExpiresAtUnixMillis: receiptExpires.UnixMilli(), Generation: uint64(enrollmentGeneration),
	}
	receiptPayload, err := agentupdateauth.EnrollmentReceiptPayload(receipt)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	receiptSignature, err := authority.Sign(receiptPayload)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_updater_enrollment_tokens SET used_at=?,receipt_expires_at=?,updater_key_id=?,activator_key_id=?,os=?,arch=?,updater_version=?,receipt_payload=?,receipt_signature=? WHERE id=? AND used_at IS NULL AND expires_at>? AND authority_key_id=? AND authority_epoch=?`, now, receiptExpires, updaterKeyID, activatorKeyID, osName, arch, strings.TrimSpace(m.UpdaterVersion), receiptPayload, receiptSignature, tokenID, now, authorityIdentity.KeyID, int64(authorityIdentity.Epoch))
	if err != nil {
		return nil, publicDBError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater enrollment token was already consumed"))
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_updater_identities (agent_id,updater_key_id,updater_public_key,activator_key_id,activator_public_key,os,arch,updater_version,trusted_root_sha256,trusted_root_version,pinned_repository,authority_key_id,authority_epoch,enrollment_generation,enrollment_receipt_payload,enrollment_receipt_signature,enabled,last_counter,last_command_sequence,last_root_action_counter,enrolled_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,0,0,0,?,?) ON CONFLICT(agent_id) DO UPDATE SET last_counter=CASE WHEN agent_updater_identities.updater_key_id=excluded.updater_key_id THEN agent_updater_identities.last_counter ELSE 0 END,last_command_sequence=CASE WHEN agent_updater_identities.authority_key_id=excluded.authority_key_id AND agent_updater_identities.authority_epoch=excluded.authority_epoch THEN agent_updater_identities.last_command_sequence ELSE 0 END,last_root_action_counter=CASE WHEN agent_updater_identities.activator_key_id=excluded.activator_key_id THEN agent_updater_identities.last_root_action_counter ELSE 0 END,updater_key_id=excluded.updater_key_id,updater_public_key=excluded.updater_public_key,activator_key_id=excluded.activator_key_id,activator_public_key=excluded.activator_public_key,os=excluded.os,arch=excluded.arch,updater_version=excluded.updater_version,trusted_root_sha256=excluded.trusted_root_sha256,trusted_root_version=excluded.trusted_root_version,pinned_repository=excluded.pinned_repository,authority_key_id=excluded.authority_key_id,authority_epoch=excluded.authority_epoch,enrollment_generation=excluded.enrollment_generation,enrollment_receipt_payload=excluded.enrollment_receipt_payload,enrollment_receipt_signature=excluded.enrollment_receipt_signature,enabled=1,enrolled_at=excluded.enrolled_at,last_seen_at=NULL,updated_at=excluded.updated_at`, agentID, updaterKeyID, m.UpdaterPublicKey, activatorKeyID, m.ActivatorPublicKey, osName, arch, strings.TrimSpace(m.UpdaterVersion), bootstrap.RootSHA256, bootstrap.RootVersion, bootstrap.PinnedRepository, authorityIdentity.KeyID, int64(authorityIdentity.Epoch), enrollmentGeneration, receiptPayload, receiptSignature, now, now)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.EnrollAgentUpdaterResponse{UpdaterKeyId: updaterKeyID, ActivatorKeyId: activatorKeyID, EnrolledAtUnixMillis: now.UnixMilli(), Receipt: enrollmentReceiptProto(receipt, receiptPayload, receiptSignature)}), nil
}

func agentUpdateKeyID(key []byte) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}

func (a *App) GetAgentUpdateOverview(ctx context.Context, req *connect.Request[p2pstreamv1.GetAgentUpdateOverviewRequest]) (*connect.Response[p2pstreamv1.GetAgentUpdateOverviewResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	resp := &p2pstreamv1.GetAgentUpdateOverviewResponse{ManagementAuthorityWarning: boundedString(a.AgentUpdateAuthorityWarning, 512)}
	if a.AgentUpdateAuthority != nil {
		resp.ManagementAuthority = agentUpdateAuthorityProto(a.AgentUpdateAuthority.Identity())
		if resp.ManagementAuthorityWarning == "" && !semver.IsValid(buildinfo.Version) {
			resp.ManagementAuthorityWarning = "managed update activation is unavailable because this management server is not running a signed semantic-version build"
		}
	} else if resp.ManagementAuthorityWarning == "" {
		resp.ManagementAuthorityWarning = "managed update authority is unavailable"
	}
	if a.TrustedAgentUpdates != nil {
		targets, err := a.TrustedAgentUpdates.ListTrustedAgentUpdateTargets(ctx)
		if err != nil {
			a.agentUpdateCatalogWarningOnce.Do(func() {
				log.Warn().Err(err).Msg("Trusted agent update catalog unavailable; returning enrollment status without release targets")
			})
		} else {
			for _, target := range targets {
				if _, err := validateAgentUpdateTarget(target); err == nil {
					resp.TrustedTargets = append(resp.TrustedTargets, target)
				}
			}
		}
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT a.id,a.public_id,a.name,COALESCE(i.enabled,0),COALESCE(i.updater_version,''),i.last_seen_at,COALESCE(x.id,0),COALESCE(x.state,'') FROM agents a LEFT JOIN agent_updater_identities i ON i.agent_id=a.id LEFT JOIN agent_update_assignments x ON x.agent_id=a.id AND (x.state NOT IN ('succeeded','failed','cancelled') OR (x.state='failed' AND x.desired_action='rollback')) ORDER BY a.id`)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item p2pstreamv1.AgentUpdateOverviewAgent
		var enrolled int64
		var state string
		var updaterLastSeen sql.NullTime
		if err := rows.Scan(&item.AgentId, &item.AgentPublicId, &item.Name, &enrolled, &item.UpdaterVersion, &updaterLastSeen, &item.ActiveAssignmentId, &state); err != nil {
			return nil, publicDBError(err)
		}
		item.UpdaterEnrolled = enrolled == 1
		item.Connected = a.AgentHub != nil && a.AgentHub.connectedByID(item.AgentId) != nil
		item.Cordoned = a.isAgentUpdateCordoned(item.AgentId)
		item.AssignmentState = assignmentStateProto(state)
		if updaterLastSeen.Valid {
			item.UpdaterLastSeenAtUnixMillis = updaterLastSeen.Time.UnixMilli()
		}
		if build, ok := a.latestAgentBuildSnapshot(item.AgentId); ok {
			item.TunnelVersion = build.Version
			item.TunnelCommit = build.Commit
		}
		resp.Agents = append(resp.Agents, &item)
	}
	return connect.NewResponse(resp), rows.Err()
}

func (a *App) PreviewAgentUpdateCampaign(ctx context.Context, req *connect.Request[p2pstreamv1.PreviewAgentUpdateCampaignRequest]) (*connect.Response[p2pstreamv1.PreviewAgentUpdateCampaignResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	target, policy, err := a.resolveAgentUpdateRequest(ctx, req.Msg.Target, req.Msg.Policy)
	if err != nil {
		return nil, err
	}
	_ = target
	items, err := a.previewAgentUpdateAgents(ctx, req.Msg.AgentIds, target, policy.MinimumEligibleAgentsPerRoute)
	if err != nil {
		return nil, err
	}
	resp := &p2pstreamv1.PreviewAgentUpdateCampaignResponse{Agents: items}
	for _, item := range items {
		if item.Eligible {
			resp.EligibleCount++
		} else {
			resp.BlockedCount++
		}
	}
	return connect.NewResponse(resp), nil
}

func (a *App) CreateAgentUpdateCampaign(ctx context.Context, req *connect.Request[p2pstreamv1.CreateAgentUpdateCampaignRequest]) (*connect.Response[p2pstreamv1.CreateAgentUpdateCampaignResponse], error) {
	user, err := a.requireAdmin(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" || len(name) > 128 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("campaign name must contain 1-128 characters"))
	}
	target, policy, err := a.resolveAgentUpdateRequest(ctx, req.Msg.Target, req.Msg.Policy)
	if err != nil {
		return nil, err
	}
	preview, err := a.previewAgentUpdateAgents(ctx, req.Msg.AgentIds, target, policy.MinimumEligibleAgentsPerRoute)
	if err != nil {
		return nil, err
	}
	agentIDs := make([]int64, 0, len(preview))
	for _, item := range preview {
		if !item.Eligible {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent %s is blocked: %s", item.AgentPublicId, strings.Join(item.Blockers, ", ")))
		}
		agentIDs = append(agentIDs, item.AgentId)
	}
	artifactsJSON, _ := json.Marshal(target.Artifacts)
	now := time.Now().UTC()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	createdBy := sql.NullInt64{Int64: user.ID, Valid: user.ID > 0}
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_by_user_id,created_at,updated_at) VALUES (?,'running',1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, name, target.Version, target.Commit, target.ManifestSha256, target.ReleaseSequence, target.RootVersion, target.SecurityEpoch, target.MinimumUpdaterVersion, target.MinimumTunnelProtocol, target.MaximumTunnelProtocol, string(artifactsJSON), policy.MaxUnavailable, policy.MinimumEligibleAgentsPerRoute, policy.CanaryCount, policy.WaveSize, policy.HealthyDwellMillis, createdBy, now, now)
	if err != nil {
		return nil, publicDBError(err)
	}
	campaignID, _ := result.LastInsertId()
	for index, agentID := range agentIDs {
		action := "none"
		if int64(index) < policy.CanaryCount {
			action = "stage"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending',?,1,?,?)`, campaignID, agentID, action, now, now); err != nil {
			return nil, publicDBError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	campaign, err := a.getAgentUpdateCampaignProto(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreateAgentUpdateCampaignResponse{Campaign: campaign}), nil
}

func (a *App) ListAgentUpdateCampaigns(ctx context.Context, req *connect.Request[p2pstreamv1.ListAgentUpdateCampaignsRequest]) (*connect.Response[p2pstreamv1.ListAgentUpdateCampaignsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	limit := req.Msg.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("limit must be between 1 and 100"))
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM agent_update_campaigns ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, publicDBError(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, publicDBError(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	resp := &p2pstreamv1.ListAgentUpdateCampaignsResponse{}
	for _, id := range ids {
		campaign, err := a.getAgentUpdateCampaignProto(ctx, id)
		if err != nil {
			return nil, err
		}
		resp.Campaigns = append(resp.Campaigns, campaign)
	}
	return connect.NewResponse(resp), nil
}

func (a *App) PauseAgentUpdateCampaign(ctx context.Context, req *connect.Request[p2pstreamv1.ChangeAgentUpdateCampaignStateRequest]) (*connect.Response[p2pstreamv1.ChangeAgentUpdateCampaignStateResponse], error) {
	return a.changeAgentUpdateCampaignState(ctx, req, "paused")
}

func (a *App) ResumeAgentUpdateCampaign(ctx context.Context, req *connect.Request[p2pstreamv1.ChangeAgentUpdateCampaignStateRequest]) (*connect.Response[p2pstreamv1.ChangeAgentUpdateCampaignStateResponse], error) {
	return a.changeAgentUpdateCampaignState(ctx, req, "running")
}

func (a *App) CancelAgentUpdateCampaign(ctx context.Context, req *connect.Request[p2pstreamv1.ChangeAgentUpdateCampaignStateRequest]) (*connect.Response[p2pstreamv1.ChangeAgentUpdateCampaignStateResponse], error) {
	return a.changeAgentUpdateCampaignState(ctx, req, "cancelled")
}

func (a *App) changeAgentUpdateCampaignState(ctx context.Context, req *connect.Request[p2pstreamv1.ChangeAgentUpdateCampaignStateRequest], state string) (*connect.Response[p2pstreamv1.ChangeAgentUpdateCampaignStateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if req.Msg.CampaignId <= 0 || req.Msg.ExpectedGeneration <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("campaign_id and expected_generation are required"))
	}
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if state == "running" {
		var blocking int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_update_assignments WHERE campaign_id=? AND state IN ('failed','blocked') AND NOT (cordoned=1 AND desired_action='rollback')`, req.Msg.CampaignId).Scan(&blocking); err != nil {
			return nil, publicDBError(err)
		}
		if blocking > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("retry or otherwise resolve failed and blocked assignments before resuming the campaign"))
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state=?,generation=generation+1,updated_at=? WHERE id=? AND generation=? AND state NOT IN ('cancelled','completed')`, state, now, req.Msg.CampaignId, req.Msg.ExpectedGeneration)
	if err != nil {
		return nil, publicDBError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, connect.NewError(connect.CodeAborted, errors.New("campaign generation changed or campaign is terminal"))
	}
	if state == "cancelled" {
		// Once an activation authorization has escaped the server it remains
		// executable until its signed expiry. Cancellation therefore cannot treat
		// activated_at=NULL as proof that the host stayed on the old binary. Keep
		// such agents cordoned and supersede the activation with a separately
		// signed rollback authorization on their next authenticated Check.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state=CASE WHEN activated_at IS NULL AND authorization_action<>'activate' AND desired_action<>'activate' THEN 'cancelled' ELSE state END,desired_action=CASE WHEN activated_at IS NULL AND authorization_action<>'activate' AND desired_action<>'activate' THEN 'none' ELSE 'rollback' END,generation=generation+1,cordoned=CASE WHEN activated_at IS NULL AND authorization_action<>'activate' AND desired_action<>'activate' THEN 0 ELSE 1 END,updated_at=? WHERE campaign_id=? AND state NOT IN ('succeeded','failed','cancelled')`, now, req.Msg.CampaignId); err != nil {
			return nil, publicDBError(err)
		}
	} else if state == "paused" {
		// A pre-authorization cordon is fully reversible: no root command has
		// escaped and the old tunnel is still running. Pausing must therefore
		// restore routing immediately instead of leaving an agent fenced until a
		// later resume/cancel operation. Cordons with any signed authorization
		// remain untouched and fail closed.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='staged',cordoned=0,updated_at=? WHERE campaign_id=? AND state='cordoned' AND desired_action='none' AND authorization_action='' AND cordoned=1`, now, req.Msg.CampaignId); err != nil {
			return nil, publicDBError(err)
		}
	} else if state == "running" {
		// Administrative pause freezes pre-authorization work. Give staging and
		// staged assignments a fresh bounded window on resume so a long, intended
		// pause cannot race the watchdog and immediately block the campaign.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE campaign_id=? AND cordoned=0 AND ((desired_action='stage' AND state IN ('pending','staging')) OR (desired_action='none' AND state='staged'))`, now, req.Msg.CampaignId); err != nil {
			return nil, publicDBError(err)
		}
	}
	type campaignCordonState struct {
		agentID  int64
		cordoned int64
	}
	var committedCordons []campaignCordonState
	if state == "cancelled" || state == "paused" {
		rows, err := tx.QueryContext(ctx, `SELECT agent_id,cordoned FROM agent_update_assignments WHERE campaign_id=?`, req.Msg.CampaignId)
		if err != nil {
			return nil, publicDBError(err)
		}
		for rows.Next() {
			var item campaignCordonState
			if err := rows.Scan(&item.agentID, &item.cordoned); err != nil {
				rows.Close()
				return nil, publicDBError(err)
			}
			committedCordons = append(committedCordons, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, publicDBError(err)
		}
		rows.Close()
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	// Publish the exact rows read in the committed transaction. This path is
	// independent of the request context and cannot leave a stale routing fence
	// if the client disconnects immediately after the durable commit.
	for _, item := range committedCordons {
		if item.cordoned == 1 {
			a.setAgentUpdateCordon(item.agentID)
		} else {
			a.clearAgentUpdateCordon(item.agentID)
		}
	}
	campaign, err := a.getAgentUpdateCampaignProto(ctx, req.Msg.CampaignId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.ChangeAgentUpdateCampaignStateResponse{Campaign: campaign}), nil
}

func (a *App) RetryAgentUpdateAssignments(ctx context.Context, req *connect.Request[p2pstreamv1.RetryAgentUpdateAssignmentsRequest]) (*connect.Response[p2pstreamv1.RetryAgentUpdateAssignmentsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if req.Msg.CampaignId <= 0 || req.Msg.ExpectedCampaignGeneration <= 0 || len(req.Msg.AssignmentIds) == 0 || len(req.Msg.AssignmentIds) > agentUpdateMaxAgents {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("campaign, generation, and bounded assignment_ids are required"))
	}
	ids, err := normalizedUniquePositiveIDs(req.Msg.AssignmentIds)
	if err != nil {
		return nil, err
	}
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var campaignState string
	var campaignGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT state,generation FROM agent_update_campaigns WHERE id=?`, req.Msg.CampaignId).Scan(&campaignState, &campaignGeneration); err != nil {
		return nil, publicDBError(err)
	}
	if campaignGeneration != req.Msg.ExpectedCampaignGeneration || (campaignState != "running" && campaignState != "paused" && campaignState != "cancelled") {
		return nil, connect.NewError(connect.CodeAborted, errors.New("campaign generation changed or is not retryable"))
	}
	type retryKind uint8
	const (
		retryStage retryKind = iota + 1
		retryRollback
	)
	retries := make(map[int64]retryKind, len(ids))
	retryAgentIDs := make(map[int64]int64, len(ids))
	for _, id := range ids {
		var state, action string
		var agentID, cordoned int64
		if err := tx.QueryRowContext(ctx, `SELECT agent_id,state,desired_action,cordoned FROM agent_update_assignments WHERE id=? AND campaign_id=?`, id, req.Msg.CampaignId).Scan(&agentID, &state, &action, &cordoned); err != nil {
			return nil, publicDBError(err)
		}
		retryAgentIDs[id] = agentID
		switch {
		case state == "blocked" && cordoned == 1:
			retries[id] = retryRollback
		case campaignState != "cancelled" && cordoned == 0 && (state == "blocked" || state == "cancelled" || (state == "failed" && action == "none")):
			retries[id] = retryStage
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("assignment %d is not retryable", id))
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET generation=generation+1,updated_at=? WHERE id=? AND generation=? AND state=?`, now, req.Msg.CampaignId, req.Msg.ExpectedCampaignGeneration, campaignState)
	if err != nil {
		return nil, publicDBError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, connect.NewError(connect.CodeAborted, errors.New("campaign generation changed or is not retryable"))
	}
	for _, id := range ids {
		var result sql.Result
		if retries[id] == retryRollback {
			result, err = tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='failed',desired_action='rollback',generation=generation+1,failure_code='',failure_detail='',authorization_action='',authorization_server_version='',command_sequence=0,authorization_nonce=X'',authorization_sha256='',authorization_payload=X'',authorization_signature=X'',authorization_issued_at=NULL,authorization_expires_at=NULL,fresh_tunnel_at=NULL,healthy_at=NULL,last_report_at=NULL,updated_at=? WHERE id=? AND campaign_id=? AND state='blocked' AND cordoned=1`, now, id, req.Msg.CampaignId)
		} else {
			// A retried stage returns to the neutral pending state. Only the
			// transactional cohort scheduler may release it, otherwise an
			// administrator retry could jump a later wave ahead of canaries.
			result, err = tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='pending',desired_action='none',generation=generation+1,cordoned=0,failure_code='',failure_detail='',attested_manifest_sha256='',attested_binary_sha256='',attested_activation_counter=0,activation_nonce_hash='',authorization_action='',authorization_server_version='',command_sequence=0,authorization_nonce=X'',authorization_sha256='',authorization_payload=X'',authorization_signature=X'',authorization_issued_at=NULL,authorization_expires_at=NULL,root_action_counter=0,root_action_receipt_payload=X'',root_action_receipt_signature=X'',root_action_completed_at=NULL,root_result_kind='',root_result_root_version=0,root_result_manifest_sha256='',root_result_version='',root_result_commit='',root_result_release_sequence=0,root_result_security_epoch=0,root_result_os='',root_result_arch='',root_result_artifact_name='',root_result_artifact_size=0,root_result_artifact_sha256='',running_version='',running_commit='',observed_version='',observed_commit='',activated_at=NULL,fresh_tunnel_at=NULL,healthy_at=NULL,last_report_at=NULL,updated_at=? WHERE id=? AND campaign_id=?`, now, id, req.Msg.CampaignId)
		}
		if err != nil {
			return nil, publicDBError(err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("assignment %d is not retryable", id))
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	for _, id := range ids {
		if retries[id] == retryRollback {
			a.setAgentUpdateCordon(retryAgentIDs[id])
		} else {
			a.clearAgentUpdateCordon(retryAgentIDs[id])
		}
	}
	campaign, err := a.getAgentUpdateCampaignProto(ctx, req.Msg.CampaignId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.RetryAgentUpdateAssignmentsResponse{Campaign: campaign}), nil
}

func (a *App) CheckAgentUpdate(ctx context.Context, req *connect.Request[p2pstreamv1.CheckAgentUpdateRequest]) (*connect.Response[p2pstreamv1.CheckAgentUpdateResponse], error) {
	release, err := a.beginAgentUpdateRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if len(req.Msg.AgentPublicId) == 0 || len(req.Msg.AgentPublicId) > 128 || req.Msg.Counter == 0 || req.Msg.Counter > math.MaxInt64 || len(req.Msg.Signature) != ed25519.SignatureSize {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid updater authentication"))
	}
	_, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return nil, err
	}
	identity, err := authenticateAgentUpdaterRequest(ctx, a.DB, req.Msg.AgentPublicId, req.Msg.Counter, req.Msg.Signature, agentUpdaterCheckSigningPayload(req.Msg))
	if err != nil {
		return nil, err
	}
	releaseIdentity, ok := a.agentUpdateIdentityRequests.tryAcquire(identity.AgentID)
	if !ok {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("another update request for this agent is already in flight"))
	}
	defer releaseIdentity()
	if !a.agentUpdateIdentityRate.allow(strconv.FormatInt(identity.AgentID, 10), time.Now().UTC()) {
		return nil, agentUpdateRateLimitError("updater identity request rate exceeded")
	}
	if err := consumeAgentUpdaterCounter(ctx, a.DB, identity.AgentID, req.Msg.Counter, time.Now().UTC()); err != nil {
		return nil, err
	}
	identity.LastCounter = int64(req.Msg.Counter)
	if identity.AuthorityKeyID != authorityIdentity.KeyID || identity.AuthorityEpoch <= 0 || uint64(identity.AuthorityEpoch) != authorityIdentity.Epoch {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("updater identity is pinned to a different management authority"))
	}
	assignment, campaign, err := a.activeAgentUpdateAssignment(ctx, identity.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewResponse(&p2pstreamv1.CheckAgentUpdateResponse{DesiredAction: p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE, RetryAfterMillis: agentUpdatePollInterval.Milliseconds(), ServerVersion: buildinfo.Version}), nil
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	a.agentUpdatesMu.Lock()
	a.reconcileAgentUpdateSuccessLocked(ctx, identity.AgentID)
	a.agentUpdatesMu.Unlock()
	assignment, campaign, err = a.activeAgentUpdateAssignment(ctx, identity.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewResponse(&p2pstreamv1.CheckAgentUpdateResponse{DesiredAction: p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE, RetryAfterMillis: agentUpdatePollInterval.Milliseconds(), ServerVersion: buildinfo.Version}), nil
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if campaign.State == "running" && (assignment.State == "staged" || (assignment.State == "cordoned" && assignment.Cordoned == 1 && assignment.DesiredAction == "none")) {
		a.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
		assignment, campaign, err = a.activeAgentUpdateAssignment(ctx, identity.AgentID)
		if err != nil {
			return nil, publicDBError(err)
		}
	}
	if campaignAllowsAgentUpdateAuthorization(campaign.State, assignment.DesiredAction) &&
		(assignment.AuthorizationAction != assignment.DesiredAction || !assignment.AuthorizationExpiresAt.Valid || !assignment.AuthorizationExpiresAt.Time.After(time.Now().UTC())) {
		assignment, campaign, err = a.ensureAgentUpdateAssignmentAuthorization(ctx, identity.AgentID, assignment.ID, assignment.Generation, assignment.DesiredAction)
		if err != nil {
			return nil, err
		}
	}
	target := campaignTargetProto(campaign)
	desiredAction := assignment.DesiredAction
	if campaign.State == "paused" || (desiredAction == "stage" && campaign.State != "running") ||
		(desiredAction == "activate" && campaign.State != "running") ||
		(desiredAction == "rollback" && campaign.State != "running" && campaign.State != "cancelled") {
		desiredAction = "none"
	}
	response := &p2pstreamv1.CheckAgentUpdateResponse{AssignmentId: assignment.ID, CampaignId: assignment.CampaignID, Generation: assignment.Generation, DesiredAction: desiredActionProto(desiredAction), Target: target, Artifact: artifactForPlatform(target.Artifacts, identity.Os, identity.Arch), RetryAfterMillis: agentUpdatePollInterval.Milliseconds(), ServerVersion: buildinfo.Version}
	if desiredAction == "activate" || desiredAction == "rollback" {
		record, recordErr := storedAssignmentAuthorization(assignment, campaign, identity)
		if recordErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, recordErr)
		}
		if string(record.Value.Action) != assignment.DesiredAction {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stored management authorization permits a different action"))
		}
		lastSequence := record.Value.CommandSequence - 1
		if verifyErr := agentupdateauth.VerifyAssignmentAuthorization(authorityIdentity.PublicKey, record.Value, record.Signature, agentupdateauth.AssignmentAuthorizationVerifyPolicy{
			Now: time.Now().UTC(), ExpectedAgentPublicID: identity.AgentPublicID, ExpectedAction: record.Value.Action,
			ExpectedAuthorityEpoch: authorityIdentity.Epoch, LastCommandSequence: lastSequence,
		}); verifyErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("stored management authorization is invalid: %w", verifyErr))
		}
		response.Authorization = assignmentAuthorizationProto(record)
	}
	return connect.NewResponse(response), nil
}

func (a *App) ReportAgentUpdate(ctx context.Context, req *connect.Request[p2pstreamv1.ReportAgentUpdateRequest]) (*connect.Response[p2pstreamv1.ReportAgentUpdateResponse], error) {
	release, err := a.beginAgentUpdateRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	m := req.Msg
	if len(m.AgentPublicId) == 0 || len(m.AgentPublicId) > 128 || m.Counter == 0 || m.Counter > math.MaxInt64 || len(m.Signature) != ed25519.SignatureSize || m.AssignmentId <= 0 || m.Generation <= 0 || len(m.ManifestSha256) > 64 || len(m.BinarySha256) > 64 || len(m.RunningVersion) > agentUpdateMaxString || len(m.RunningCommit) > agentUpdateMaxString || len(m.FailureCode) > 128 || len(m.FailureDetail) > agentUpdateMaxFailureDetail || len(m.ActivationNonce) > 64 || len(m.ActivatorSignature) > ed25519.SignatureSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid or oversized update report"))
	}
	if (m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED || m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK) &&
		(m.ActivationCounter != 0 || len(m.ActivationNonce) != 0 || len(m.ActivatorSignature) != 0 || m.RootActionReceipt == nil) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("root action reports require the signed root_action_receipt and no legacy attestation fields"))
	}
	_, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return nil, err
	}
	// Signature verification is read-only. The replay counters are consumed in
	// the same transaction as the assignment transition below so a failed write
	// never strands an otherwise retryable signed report.
	identity, err := authenticateAgentUpdaterRequest(ctx, a.DB, m.AgentPublicId, m.Counter, m.Signature, agentUpdaterReportSigningPayload(m))
	if err != nil {
		return nil, err
	}
	releaseIdentity, ok := a.agentUpdateIdentityRequests.tryAcquire(identity.AgentID)
	if !ok {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("another update request for this agent is already in flight"))
	}
	defer releaseIdentity()
	if !a.agentUpdateIdentityRate.allow(strconv.FormatInt(identity.AgentID, 10), time.Now().UTC()) {
		return nil, agentUpdateRateLimitError("updater identity request rate exceeded")
	}
	if identity.AuthorityKeyID != authorityIdentity.KeyID || identity.AuthorityEpoch <= 0 || uint64(identity.AuthorityEpoch) != authorityIdentity.Epoch {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("updater identity is pinned to a different management authority"))
	}
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	// Refresh monotonic counters after entering the serialized transaction. Two
	// reports may both authenticate before either acquires agentUpdatesMu; using
	// the pre-lock row would misclassify a committed lost-response retry.
	identity, err = agentUpdaterIdentityByAgentID(ctx, tx, identity.AgentID)
	if err != nil {
		return nil, publicDBError(err)
	}
	assignment, campaign, err := activeAgentUpdateAssignmentQuery(ctx, tx, identity.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return a.acknowledgeSupersededAgentUpdateReport(ctx, tx, identity, m)
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if assignment.ID != m.AssignmentId || assignment.Generation != m.Generation {
		if assignment.ID == m.AssignmentId {
			return a.acknowledgeSupersededAgentUpdateReportRow(ctx, tx, identity, m, assignment.State, assignment.DesiredAction, assignment.Generation)
		}
		return a.acknowledgeSupersededAgentUpdateReport(ctx, tx, identity, m)
	}
	if assignment.State == "blocked" && assignment.DesiredAction == "none" {
		matchesCommittedFailure := m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED &&
			m.FailureCode == assignment.FailureCode && m.FailureDetail == assignment.FailureDetail
		matchesCommittedHealth := m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY &&
			assignment.RootActionCompletedAt.Valid && m.ManifestSha256 == assignment.RootResultManifestSha256 &&
			m.BinarySha256 == assignment.RootResultArtifactSha256 && m.RunningVersion == assignment.RootResultVersion &&
			m.RunningCommit == assignment.RootResultCommit
		if matchesCommittedFailure || matchesCommittedHealth {
			if m.Counter < uint64(identity.LastCounter) {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater counter was replayed"))
			}
			if m.Counter > uint64(identity.LastCounter) {
				if err := consumeAgentUpdaterCounter(ctx, tx, identity.AgentID, m.Counter, time.Now().UTC()); err != nil {
					return nil, err
				}
			}
			if err := tx.Commit(); err != nil {
				return nil, publicDBError(err)
			}
			return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{State: assignmentStateProto(assignment.State), DesiredAction: desiredActionProto(assignment.DesiredAction), Generation: assignment.Generation, RetryAfterMillis: agentUpdatePollInterval.Milliseconds()}), nil
		}
	}
	now := time.Now().UTC()
	state, action, cordoned := assignment.State, assignment.DesiredAction, assignment.Cordoned
	pauseCampaignForFailure := false
	isActivation := m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED
	isRollback := m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK
	platformArtifact := artifactForPlatform(campaign.Artifacts, identity.Os, identity.Arch)
	if platformArtifact == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("campaign has no artifact for the enrolled updater platform"))
	}
	var rootReceipt agentupdateauth.RootActionReceipt
	var rootReceiptPayload, rootReceiptSignature []byte
	if isActivation || isRollback {
		expectedAction := agentupdateauth.AssignmentActionActivate
		if isRollback {
			expectedAction = agentupdateauth.AssignmentActionRollback
		}
		candidate, candidatePayload, candidateSignature, candidateErr := rootActionReceiptFromProto(m.RootActionReceipt)
		if candidateErr != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, candidateErr)
		}
		if assignment.RootActionCompletedAt.Valid && assignment.AuthorizationAction == string(expectedAction) &&
			assignment.RootActionCounter == int64(candidate.RootActionCounter) && identity.LastRootActionCounter == int64(candidate.RootActionCounter) &&
			bytes.Equal(assignment.RootActionReceiptPayload, candidatePayload) && bytes.Equal(assignment.RootActionReceiptSignature, candidateSignature) &&
			ed25519.Verify(ed25519.PublicKey(identity.ActivatorPublicKey), candidatePayload, candidateSignature) {
			// The root action already committed and the updater is recovering a
			// lost response. Exact stored canonical bytes make this idempotent; a
			// newer worker counter is consumed, while an exact HTTP retry may reuse
			// the last counter without mutating durable state.
			if m.ManifestSha256 != candidate.ResultManifestSHA256 || m.BinarySha256 != candidate.ResultArtifactSHA256 || m.RunningVersion != candidate.ResultVersion || m.RunningCommit != candidate.ResultCommit || m.Counter < uint64(identity.LastCounter) {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("root action report retry does not match the committed receipt"))
			}
			if m.Counter > uint64(identity.LastCounter) {
				if err := consumeAgentUpdaterCounter(ctx, tx, identity.AgentID, m.Counter, time.Now().UTC()); err != nil {
					return nil, err
				}
			}
			if err := tx.Commit(); err != nil {
				return nil, publicDBError(err)
			}
			return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{State: assignmentStateProto(assignment.State), DesiredAction: desiredActionProto(assignment.DesiredAction), Generation: assignment.Generation, RetryAfterMillis: agentUpdatePollInterval.Milliseconds()}), nil
		}
		rootReceipt, rootReceiptPayload, rootReceiptSignature, err = verifyAgentUpdateRootActionReceipt(identity, assignment, campaign, m.RootActionReceipt, expectedAction, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if m.ManifestSha256 != rootReceipt.ResultManifestSHA256 || m.BinarySha256 != rootReceipt.ResultArtifactSHA256 || m.RunningVersion != rootReceipt.ResultVersion || m.RunningCommit != rootReceipt.ResultCommit {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("root action report does not match its signed result receipt"))
		}
	} else if m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY {
		if !assignment.ActivatedAt.Valid || assignment.RootResultKind != string(agentupdateauth.RootActionResultSignedRelease) || assignment.RootActionReceiptPayload == nil || m.ManifestSha256 != assignment.RootResultManifestSha256 || m.BinarySha256 != assignment.RootResultArtifactSha256 || m.RunningVersion != assignment.RootResultVersion || m.RunningCommit != assignment.RootResultCommit {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("healthy report does not match the stored root activation attestation"))
		}
	}
	switch m.State {
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_DOWNLOADING:
		if action != "stage" || (state != "pending" && state != "staging") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assignment is not requesting staging"))
		}
		state = "staging"
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED:
		if action != "stage" || (state != "pending" && state != "staging") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assignment is not accepting a staged result"))
		}
		if m.ManifestSha256 != campaign.ManifestSha256 || m.BinarySha256 != platformArtifact.Sha256 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("staged digests do not match target"))
		}
		state, action = "staged", "none"
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED:
		if action != "activate" || cordoned != 1 || (state != "cordoned" && state != "activating") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assignment is not cordoned for activation"))
		}
		state, action = "awaiting_tunnel", "none"
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY:
		if !assignment.ActivatedAt.Valid || (state != "awaiting_tunnel" && state != "healthy_dwell") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("activation was not recorded"))
		}
		state = "healthy_dwell"
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED:
		switch {
		case action == "stage" && cordoned == 0 && (state == "pending" || state == "staging"):
			// Staging intentionally runs ahead of activation cohorts. Any accepted
			// staging failure must therefore pause immediately; otherwise a future
			// cohort can fail before its predecessors and leave a running campaign
			// with no active assignment capable of waking the scheduler later.
			pauseCampaignForFailure = true
			state, action = "failed", "none"
		case action == "activate" && cordoned == 1 && (state == "cordoned" || state == "activating"):
			// The updater key is deliberately lower privilege than the offline
			// activator. It may quarantine its own assignment, but it cannot cause
			// management to mint a root-authorized rollback. An administrator must
			// explicitly retry this blocked, still-cordoned assignment.
			state, action, pauseCampaignForFailure = "blocked", "none", true
		case action == "none" && cordoned == 1 && assignment.ActivatedAt.Valid && (state == "awaiting_tunnel" || state == "healthy_dwell"):
			state, action, pauseCampaignForFailure = "blocked", "none", true
		case action == "rollback" && cordoned == 1 && (state == "failed" || state == "cordoned" || state == "activating" || state == "awaiting_tunnel" || state == "healthy_dwell"):
			state, action, pauseCampaignForFailure = "blocked", "none", true
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assignment is not accepting a failure report in its current phase"))
		}
	case p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK:
		if action != "rollback" || cordoned != 1 || (state != "failed" && state != "cordoned" && state != "activating" && state != "awaiting_tunnel" && state != "healthy_dwell") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("assignment is not cordoned for rollback"))
		}
		state, action, cordoned = "awaiting_tunnel", "none", 1
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported updater report state"))
	}
	activatedAt := assignment.ActivatedAt
	healthyAt := assignment.HealthyAt
	freshTunnelAt := assignment.FreshTunnelAt
	if isActivation {
		activatedAt = sql.NullTime{Time: now, Valid: true}
	}
	if m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY && !healthyAt.Valid {
		healthyAt = sql.NullTime{Time: now, Valid: true}
	}
	authorizationAction := assignment.AuthorizationAction
	authorizationServerVersion := assignment.AuthorizationServerVersion
	commandSequence := assignment.CommandSequence
	authorizationNonce := assignment.AuthorizationNonce
	authorizationSHA256 := assignment.AuthorizationSha256
	authorizationPayload := assignment.AuthorizationPayload
	authorizationSignature := assignment.AuthorizationSignature
	authorizationIssuedAt := assignment.AuthorizationIssuedAt
	authorizationExpiresAt := assignment.AuthorizationExpiresAt
	nonceHash := assignment.ActivationNonceHash
	attestedManifest := assignment.AttestedManifestSha256
	attestedBinary := assignment.AttestedBinarySha256
	attestedCounter := assignment.AttestedActivationCounter
	runningVersion, runningCommit := assignment.RunningVersion, assignment.RunningCommit
	rootActionCounter := assignment.RootActionCounter
	rootActionPayload := assignment.RootActionReceiptPayload
	rootActionSignature := assignment.RootActionReceiptSignature
	rootActionCompletedAt := assignment.RootActionCompletedAt
	rootResultKind := assignment.RootResultKind
	rootResultRootVersion := assignment.RootResultRootVersion
	rootResultManifest := assignment.RootResultManifestSha256
	rootResultVersion, rootResultCommit := assignment.RootResultVersion, assignment.RootResultCommit
	rootResultSequence, rootResultSecurityEpoch := assignment.RootResultReleaseSequence, assignment.RootResultSecurityEpoch
	rootResultOS, rootResultArch := assignment.RootResultOs, assignment.RootResultArch
	rootResultArtifactName, rootResultArtifactSize, rootResultArtifactSHA256 := assignment.RootResultArtifactName, assignment.RootResultArtifactSize, assignment.RootResultArtifactSha256
	if isActivation || isRollback {
		d := sha256.Sum256(rootReceipt.AuthorizationNonce)
		nonceHash = hex.EncodeToString(d[:])
		attestedManifest, attestedBinary, attestedCounter = rootReceipt.ResultManifestSHA256, rootReceipt.ResultArtifactSHA256, int64(rootReceipt.RootActionCounter)
		runningVersion, runningCommit = rootReceipt.ResultVersion, rootReceipt.ResultCommit
		rootActionCounter, rootActionPayload, rootActionSignature = int64(rootReceipt.RootActionCounter), rootReceiptPayload, rootReceiptSignature
		rootActionCompletedAt = sql.NullTime{Time: now, Valid: true}
		rootResultKind, rootResultRootVersion = string(rootReceipt.ResultKind), int64(rootReceipt.ResultRootVersion)
		rootResultManifest, rootResultVersion, rootResultCommit = rootReceipt.ResultManifestSHA256, rootReceipt.ResultVersion, rootReceipt.ResultCommit
		rootResultSequence, rootResultSecurityEpoch = int64(rootReceipt.ResultReleaseSequence), int64(rootReceipt.ResultSecurityEpoch)
		rootResultOS, rootResultArch = rootReceipt.ResultOS, rootReceipt.ResultArch
		rootResultArtifactName, rootResultArtifactSize, rootResultArtifactSHA256 = rootReceipt.ResultArtifactName, rootReceipt.ResultArtifactSize, rootReceipt.ResultArtifactSHA256
		freshTunnelAt, healthyAt = sql.NullTime{}, sql.NullTime{}
	}
	if err := consumeAgentUpdaterCounter(ctx, tx, identity.AgentID, m.Counter, now); err != nil {
		return nil, err
	}
	if isActivation || isRollback {
		if err := consumeRootActionCounter(ctx, tx, identity, rootReceipt.RootActionCounter, now); err != nil {
			return nil, err
		}
	}
	assignmentUpdatedAt := now
	preserveEvidenceAnchor := m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_DOWNLOADING ||
		(m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY && assignment.State == "healthy_dwell" && assignment.HealthyAt.Valid)
	if preserveEvidenceAnchor && !assignment.UpdatedAt.IsZero() {
		// updated_at is the server-owned start of the bounded staging window and,
		// after activation, the latest first-valid evidence timestamp. Untrusted
		// progress telemetry and idempotent HEALTHY retries must not extend either
		// deadline; last_report_at still records every authenticated heartbeat.
		assignmentUpdatedAt = assignment.UpdatedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state=?,desired_action=?,cordoned=?,failure_code=?,failure_detail=?,attested_manifest_sha256=?,attested_binary_sha256=?,attested_activation_counter=?,activation_nonce_hash=?,authorization_action=?,authorization_server_version=?,command_sequence=?,authorization_nonce=?,authorization_sha256=?,authorization_payload=?,authorization_signature=?,authorization_issued_at=?,authorization_expires_at=?,root_action_counter=?,root_action_receipt_payload=?,root_action_receipt_signature=?,root_action_completed_at=?,root_result_kind=?,root_result_root_version=?,root_result_manifest_sha256=?,root_result_version=?,root_result_commit=?,root_result_release_sequence=?,root_result_security_epoch=?,root_result_os=?,root_result_arch=?,root_result_artifact_name=?,root_result_artifact_size=?,root_result_artifact_sha256=?,running_version=?,running_commit=?,observed_version=?,observed_commit=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,last_report_at=?,updated_at=? WHERE id=? AND generation=?`, state, action, cordoned, boundedString(m.FailureCode, 128), boundedString(m.FailureDetail, agentUpdateMaxFailureDetail), attestedManifest, attestedBinary, attestedCounter, nonceHash, authorizationAction, authorizationServerVersion, commandSequence, authorizationNonce, authorizationSHA256, authorizationPayload, authorizationSignature, authorizationIssuedAt, authorizationExpiresAt, rootActionCounter, rootActionPayload, rootActionSignature, rootActionCompletedAt, rootResultKind, rootResultRootVersion, rootResultManifest, rootResultVersion, rootResultCommit, rootResultSequence, rootResultSecurityEpoch, rootResultOS, rootResultArch, rootResultArtifactName, rootResultArtifactSize, rootResultArtifactSHA256, runningVersion, runningCommit, chooseString(isActivation || isRollback, "", assignment.ObservedVersion), chooseString(isActivation || isRollback, "", assignment.ObservedCommit), activatedAt, freshTunnelAt, healthyAt, now, assignmentUpdatedAt, assignment.ID, assignment.Generation)
	if err != nil {
		return nil, publicDBError(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, connect.NewError(connect.CodeAborted, errors.New("assignment generation changed"))
	}
	if m.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED && pauseCampaignForFailure {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, assignment.CampaignID); err != nil {
			return nil, publicDBError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	a.appendAgentUpdateEvent(ctx, assignment.CampaignID, assignment.ID, identity.AgentID, "updater_report", boundedString(m.FailureCode, 128))
	if isActivation || isRollback {
		// The activator restarts and health-checks the agent before it can report
		// the durable activation attestation. Force the tunnel that was established
		// before this committed edge to reconnect so fresh_tunnel_at can only be
		// satisfied by a post-attestation session. Resolve by ID after the commit so
		// a connection racing with either activation or rollback report cannot
		// evade the post-root-action freshness gate.
		a.revokeAgentConnection(identity.AgentID)
	}
	if cordoned == 0 {
		a.clearAgentUpdateCordon(identity.AgentID)
	}
	if state == "staged" && campaign.State == "running" {
		assignment.State, assignment.DesiredAction = state, action
		a.tryAdvanceAgentUpdateAssignmentLocked(ctx, assignment, campaign)
	}
	a.reconcileAgentUpdateSuccessLocked(ctx, identity.AgentID)
	updated, _, err := a.activeAgentUpdateAssignment(ctx, identity.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{State: p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_SUCCEEDED, DesiredAction: p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE, Generation: assignment.Generation, RetryAfterMillis: agentUpdatePollInterval.Milliseconds()}), nil
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{State: assignmentStateProto(updated.State), DesiredAction: desiredActionProto(updated.DesiredAction), Generation: updated.Generation, RetryAfterMillis: agentUpdatePollInterval.Milliseconds()}), nil
}

// acknowledgeSupersededAgentUpdateReport drains a durable host-side result
// after management has advanced or terminated the assignment generation. The
// report is still authenticated by the updater key, but it cannot mutate the
// newer assignment or consume a root-action counter. Returning the current
// state lets the worker archive the obsolete local record and poll the newer
// signed action (most importantly cancellation-triggered rollback).
func (a *App) acknowledgeSupersededAgentUpdateReport(ctx context.Context, tx *sql.Tx, identity agentUpdaterIdentityRow, m *p2pstreamv1.ReportAgentUpdateRequest) (*connect.Response[p2pstreamv1.ReportAgentUpdateResponse], error) {
	var state, action string
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT state,desired_action,generation FROM agent_update_assignments WHERE id=? AND agent_id=?`, m.AssignmentId, identity.AgentID).Scan(&state, &action, &generation)
	if err != nil {
		return nil, publicDBError(err)
	}
	if m.Generation > generation {
		return nil, connect.NewError(connect.CodeAborted, errors.New("assignment generation changed"))
	}
	return a.acknowledgeSupersededAgentUpdateReportRow(ctx, tx, identity, m, state, action, generation)
}

func (a *App) acknowledgeSupersededAgentUpdateReportRow(ctx context.Context, tx *sql.Tx, identity agentUpdaterIdentityRow, m *p2pstreamv1.ReportAgentUpdateRequest, state, action string, generation int64) (*connect.Response[p2pstreamv1.ReportAgentUpdateResponse], error) {
	if m.Generation >= generation && state != "succeeded" && state != "failed" && state != "cancelled" {
		return nil, connect.NewError(connect.CodeAborted, errors.New("assignment generation changed"))
	}
	if m.Counter < uint64(identity.LastCounter) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("updater counter was replayed"))
	}
	if m.Counter > uint64(identity.LastCounter) {
		if err := consumeAgentUpdaterCounter(ctx, tx, identity.AgentID, m.Counter, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{
		State: assignmentStateProto(state), DesiredAction: desiredActionProto(action), Generation: generation,
		RetryAfterMillis: agentUpdatePollInterval.Milliseconds(),
	}), nil
}

func (a *App) beginAgentUpdateRequest(ctx context.Context) (func(), error) {
	if ctx != nil {
		if admitted, _ := ctx.Value(agentUpdateAdmissionContextKey{}).(bool); admitted {
			return func() {}, nil
		}
	}
	if a == nil || a.agentUpdateRequestGate == nil {
		return func() {}, nil
	}
	select {
	case a.agentUpdateRequestGate <- struct{}{}:
		return func() { <-a.agentUpdateRequestGate }, nil
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("agent update control plane is busy"))
	}
}

// agentUpdateHTTPAdmission reserves bounded work before Connect decodes or
// decompresses attacker-controlled updater messages. Per-peer admission keeps
// one source from occupying the entire fleet control lane, while the method
// handler adds a per-enrolled-agent serialization boundary after signature
// verification.
func (a *App) agentUpdateHTTPAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var maxBytes int64
		switch r.URL.Path {
		case p2pstreamv1connect.AgentManagementServiceEnrollAgentUpdaterProcedure:
			maxBytes = agentUpdateEnrollMaxRequestBytes
		case p2pstreamv1connect.AgentManagementServiceCheckAgentUpdateProcedure:
			maxBytes = agentUpdateCheckMaxRequestBytes
		case p2pstreamv1connect.AgentManagementServiceReportAgentUpdateProcedure:
			maxBytes = agentUpdateReportMaxRequestBytes
		default:
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxBytes {
			http.Error(w, "agent update request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		// The shared Connect handler retains a larger compatibility ceiling for
		// authenticated management APIs. Update-control messages are tiny, so deny
		// compressed request bodies here to keep their pre-decode bound equal to
		// the method-specific wire bound instead of permitting a compression bomb.
		for _, header := range []string{"Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
			encoding := strings.TrimSpace(r.Header.Get(header))
			if encoding != "" && !strings.EqualFold(encoding, "identity") {
				http.Error(w, "compressed agent update requests are not accepted", http.StatusUnsupportedMediaType)
				return
			}
		}
		peer := r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			peer = host
		}
		now := time.Now().UTC()
		if !a.agentUpdatePeerRate.allow(peer, now) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "managed update peer request rate exceeded", http.StatusTooManyRequests)
			return
		}
		// Charge a source-specific bucket first. Once one source is throttled it
		// can no longer drain the fleet-wide budget and suppress other peers.
		if !a.agentUpdateGlobalRate.allow("global", now) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "managed update request rate exceeded", http.StatusTooManyRequests)
			return
		}
		releasePeer, ok := a.agentUpdatePeerRequests.tryAcquire(peer, -1)
		if !ok {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "agent update peer is busy", http.StatusTooManyRequests)
			return
		}
		defer releasePeer()
		releaseGlobal, err := a.beginAgentUpdateRequest(r.Context())
		if err != nil {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "agent update control plane is busy", http.StatusServiceUnavailable)
			return
		}
		defer releaseGlobal()
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		r = r.WithContext(context.WithValue(r.Context(), agentUpdateAdmissionContextKey{}, true))
		next.ServeHTTP(w, r)
	})
}

func agentUpdateRateLimitError(message string) error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New(message))
	err.Meta().Set("Retry-After", "5")
	return err
}

// StartAgentUpdateMaintenance runs deadline and cohort reconciliation from a
// server-owned clock. Safety transitions must not depend on the very updater
// worker or candidate tunnel that may have failed.
func (a *App) StartAgentUpdateMaintenance(ctx context.Context) {
	if a == nil || a.DB == nil || !a.agentUpdateMaintenanceStarted.CompareAndSwap(false, true) {
		return
	}
	go func() {
		a.reconcileAgentUpdateMaintenance(context.Background(), time.Now().UTC())
		ticker := time.NewTicker(agentUpdateMaintenanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				a.reconcileAgentUpdateMaintenance(context.Background(), now.UTC())
			}
		}
	}()
}

type agentUpdateMaintenanceItem struct {
	id, campaignID, agentID int64
	state, action           string
	campaignState           string
	cordoned                int64
	updated                 time.Time
	rootCompleted           sql.NullTime
}

func (a *App) reconcileAgentUpdateMaintenance(ctx context.Context, now time.Time) {
	if a == nil || a.DB == nil {
		return
	}
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()

	rows, err := a.DB.QueryContext(ctx, `SELECT x.id,x.campaign_id,x.agent_id,x.state,x.desired_action,c.state,x.cordoned,x.updated_at,x.root_action_completed_at FROM agent_update_assignments x JOIN agent_update_campaigns c ON c.id=x.campaign_id WHERE (x.state NOT IN ('succeeded','failed','cancelled') OR (x.state='failed' AND x.desired_action='rollback')) ORDER BY x.id`)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to scan managed-update maintenance state")
		return
	}
	items := make([]agentUpdateMaintenanceItem, 0)
	for rows.Next() {
		var item agentUpdateMaintenanceItem
		if err := rows.Scan(&item.id, &item.campaignID, &item.agentID, &item.state, &item.action, &item.campaignState, &item.cordoned, &item.updated, &item.rootCompleted); err != nil {
			rows.Close()
			log.Warn().Err(err).Msg("Failed to decode managed-update maintenance state")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Warn().Err(err).Msg("Failed while scanning managed-update maintenance state")
		return
	}
	rows.Close()

	campaigns := make(map[int64]agentUpdateCampaignRow)
	campaignAgents := make(map[int64]int64)
	for _, item := range items {
		campaignAgents[item.campaignID] = item.agentID
		switch {
		case item.state == "cordoned" && item.action == "none":
			x, c, err := activeAgentUpdateAssignmentQuery(ctx, a.DB, item.agentID)
			if err != nil || x.ID != item.id {
				continue
			}
			campaigns[item.campaignID] = c
			a.tryAdvanceAgentUpdateAssignmentLocked(ctx, x, c)
		case item.campaignState == "running" && item.cordoned == 0 && item.state == "staged" && item.action == "none":
			// A completed stage may legitimately wait behind a long healthy dwell
			// or max-unavailable slot. It has no fixed staging deadline while
			// queued; instead, idempotently attempt admission whenever maintenance
			// observes it. Once eligible it enters the separately bounded drain and
			// root-action phases.
			x, c, err := activeAgentUpdateAssignmentQuery(ctx, a.DB, item.agentID)
			if err != nil || x.ID != item.id {
				continue
			}
			campaigns[item.campaignID] = c
			a.tryAdvanceAgentUpdateAssignmentLocked(ctx, x, c)
		case item.rootCompleted.Valid && (item.state == "awaiting_tunnel" || item.state == "healthy_dwell"):
			a.reconcileAgentUpdateSuccessLocked(ctx, item.agentID)
		case item.campaignState == "running" && item.cordoned == 0 &&
			item.action == "stage" && (item.state == "pending" || item.state == "staging") &&
			!now.Before(item.updated.Add(agentUpdateStageTimeout)):
			a.blockTimedOutAgentUpdateStageLocked(ctx, item, now)
		case item.cordoned == 1 && (item.action == "activate" || item.action == "rollback") && !item.rootCompleted.Valid && !now.Before(item.updated.Add(agentUpdatePostActionTimeout)):
			a.blockTimedOutAgentUpdateRootActionLocked(ctx, item, now)
		}
	}
	for campaignID, agentID := range campaignAgents {
		campaign, ok := campaigns[campaignID]
		if !ok {
			_, campaign, err = activeAgentUpdateAssignmentQuery(ctx, a.DB, agentID)
			if err != nil {
				continue
			}
		}
		if campaign.State == "running" {
			if err := a.releaseNextAgentUpdateStageCohortLocked(ctx, campaignID, campaign); err != nil {
				log.Warn().Err(err).Int64("campaign_id", campaignID).Msg("Failed to reconcile managed-update staging cohort")
			}
		}
	}
	if err := a.finalizeCompletedAgentUpdateCampaignsLocked(ctx, now); err != nil {
		log.Warn().Err(err).Msg("Failed to finalize completed managed-update campaigns")
	}
}

func (a *App) finalizeCompletedAgentUpdateCampaignsLocked(ctx context.Context, now time.Time) error {
	_, err := a.DB.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='completed',generation=generation+1,completed_at=?,updated_at=? WHERE state='running' AND NOT EXISTS (SELECT 1 FROM agent_update_assignments x WHERE x.campaign_id=agent_update_campaigns.id AND (x.state NOT IN ('succeeded','failed','cancelled') OR (x.state='failed' AND x.desired_action='rollback')))`, now, now)
	return err
}

func (a *App) blockTimedOutAgentUpdateStageLocked(ctx context.Context, item agentUpdateMaintenanceItem, now time.Time) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='blocked',desired_action='none',failure_code='stage_timeout',failure_detail='staging or activation readiness did not complete before the deadline',updated_at=? WHERE id=? AND state=? AND desired_action=? AND cordoned=0 AND updated_at=?`, now, item.id, item.state, item.action, item.updated)
	if err != nil {
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, item.campaignID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	a.appendAgentUpdateEvent(ctx, item.campaignID, item.id, item.agentID, "stage_timeout", "campaign paused; retry required")
}

func (a *App) blockTimedOutAgentUpdateRootActionLocked(ctx context.Context, item agentUpdateMaintenanceItem, now time.Time) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='blocked',desired_action='none',failure_code='root_action_timeout',failure_detail='the signed root action did not complete before the deadline',updated_at=? WHERE id=? AND desired_action=? AND cordoned=1 AND root_action_completed_at IS NULL AND updated_at=?`, now, item.id, item.action, item.updated)
	if err != nil {
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, item.campaignID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	a.appendAgentUpdateEvent(ctx, item.campaignID, item.id, item.agentID, "root_action_timeout", "signed action removed from desired state; escaped authorization remains possible, so the agent stays cordoned and requires administrator rollback")
}

func (a *App) resolveAgentUpdateRequest(ctx context.Context, requested *p2pstreamv1.AgentUpdateTarget, policy *p2pstreamv1.AgentUpdatePolicy) (*p2pstreamv1.AgentUpdateTarget, *p2pstreamv1.AgentUpdatePolicy, error) {
	if _, _, err := a.requireAgentUpdateAuthority(); err != nil {
		return nil, nil, err
	}
	if !semver.IsValid(buildinfo.Version) {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management server build version is not a signed-release semantic version"))
	}
	if requested == nil || !agentUpdateDigestRE.MatchString(requested.ManifestSha256) {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted manifest_sha256 is required"))
	}
	if a.TrustedAgentUpdates == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("trusted agent update catalog is not configured"))
	}
	trusted, err := a.TrustedAgentUpdates.ResolveTrustedAgentUpdateTarget(ctx, requested.ManifestSha256)
	if err != nil || trusted == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("manifest digest is not present in the trusted update catalog"))
	}
	trusted, err = validateAgentUpdateTarget(trusted)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("trusted update target is invalid: %w", err))
	}
	if trusted.ManifestSha256 != requested.ManifestSha256 {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("trusted update catalog returned a different manifest digest"))
	}
	normalized, err := normalizeAgentUpdatePolicy(policy)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return trusted, normalized, nil
}

func validateAgentUpdateTarget(target *p2pstreamv1.AgentUpdateTarget) (*p2pstreamv1.AgentUpdateTarget, error) {
	if target == nil || !agentUpdateDigestRE.MatchString(target.ManifestSha256) || !semver.IsValid(target.Version) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(target.Commit) || target.ReleaseSequence <= 0 || target.RootVersion <= 0 || target.SecurityEpoch <= 0 || !semver.IsValid(target.MinimumUpdaterVersion) || target.MinimumTunnelProtocol <= 0 || target.MaximumTunnelProtocol < target.MinimumTunnelProtocol || int64(tunnel.ProtocolVersion) < target.MinimumTunnelProtocol || int64(tunnel.ProtocolVersion) > target.MaximumTunnelProtocol || len(target.Artifacts) == 0 || len(target.Artifacts) > agentUpdateMaxArtifacts {
		return nil, errors.New("target fields are incomplete or out of bounds")
	}
	seen := make(map[string]struct{}, len(target.Artifacts))
	for _, artifact := range target.Artifacts {
		if artifact == nil || !agentUpdatePlatformRE.MatchString(artifact.Os) || !agentUpdatePlatformRE.MatchString(artifact.Arch) || !agentUpdateArtifactNameRE.MatchString(artifact.Name) || !agentUpdateDigestRE.MatchString(artifact.Sha256) || artifact.SizeBytes <= 0 || artifact.SizeBytes > agentupdate.MaxArtifactSize {
			return nil, errors.New("invalid target artifact")
		}
		key := artifact.Os + "/" + artifact.Arch
		if _, ok := seen[key]; ok {
			return nil, errors.New("duplicate target artifact platform")
		}
		seen[key] = struct{}{}
	}
	return proto.Clone(target).(*p2pstreamv1.AgentUpdateTarget), nil
}

func normalizeAgentUpdatePolicy(policy *p2pstreamv1.AgentUpdatePolicy) (*p2pstreamv1.AgentUpdatePolicy, error) {
	if policy == nil {
		policy = &p2pstreamv1.AgentUpdatePolicy{}
	}
	result := &p2pstreamv1.AgentUpdatePolicy{
		MaxUnavailable:                policy.MaxUnavailable,
		MinimumEligibleAgentsPerRoute: policy.MinimumEligibleAgentsPerRoute,
		CanaryCount:                   policy.CanaryCount,
		WaveSize:                      policy.WaveSize,
		HealthyDwellMillis:            policy.HealthyDwellMillis,
	}
	if result.MaxUnavailable == 0 {
		result.MaxUnavailable = 1
	}
	if result.MinimumEligibleAgentsPerRoute == 0 {
		result.MinimumEligibleAgentsPerRoute = 1
	}
	if result.CanaryCount == 0 {
		result.CanaryCount = 1
	}
	if result.WaveSize == 0 {
		result.WaveSize = 1
	}
	if result.HealthyDwellMillis == 0 {
		result.HealthyDwellMillis = int64((2 * time.Minute) / time.Millisecond)
	}
	if result.MaxUnavailable < 1 || result.MaxUnavailable > 100 || result.MinimumEligibleAgentsPerRoute < 1 || result.MinimumEligibleAgentsPerRoute > 100 || result.CanaryCount < 1 || result.CanaryCount > 100 || result.WaveSize < 1 || result.WaveSize > 100 || result.HealthyDwellMillis < 10_000 || result.HealthyDwellMillis > int64((24*time.Hour)/time.Millisecond) {
		return nil, errors.New("update policy is outside safe bounds")
	}
	return result, nil
}

func normalizedUniquePositiveIDs(values []int64) ([]int64, error) {
	if len(values) == 0 || len(values) > agentUpdateMaxAgents {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a bounded non-empty id list is required"))
	}
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ids must be positive"))
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (a *App) previewAgentUpdateAgents(ctx context.Context, requested []int64, target *p2pstreamv1.AgentUpdateTarget, minimum int64) ([]*p2pstreamv1.AgentUpdatePreviewAgent, error) {
	ids, err := normalizedUniquePositiveIDs(requested)
	if err != nil {
		return nil, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	arguments := make([]any, len(ids))
	for i, id := range ids {
		arguments[i] = id
	}
	type previewAgentRow struct {
		id                     int64
		publicID, name         string
		enabled, enrolled      int64
		updaterOS, updaterArch string
		updaterVersion         string
		updaterLastSeen        sql.NullTime
	}
	agents := make(map[int64]previewAgentRow, len(ids))
	rows, err := a.DB.QueryContext(ctx, `SELECT a.id,a.public_id,a.name,a.enabled,COALESCE(i.enabled,0),COALESCE(i.os,''),COALESCE(i.arch,''),COALESCE(i.updater_version,''),i.last_seen_at FROM agents a LEFT JOIN agent_updater_identities i ON i.agent_id=a.id WHERE a.id IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return nil, publicDBError(err)
	}
	for rows.Next() {
		var row previewAgentRow
		if err := rows.Scan(&row.id, &row.publicID, &row.name, &row.enabled, &row.enrolled, &row.updaterOS, &row.updaterArch, &row.updaterVersion, &row.updaterLastSeen); err != nil {
			rows.Close()
			return nil, publicDBError(err)
		}
		agents[row.id] = row
	}
	if err := rows.Close(); err != nil {
		return nil, publicDBError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, publicDBError(err)
	}
	if len(agents) != len(ids) {
		return nil, publicDBError(sql.ErrNoRows)
	}
	activeAssignments := make(map[int64]int64, len(ids))
	rows, err = a.DB.QueryContext(ctx, `SELECT agent_id,COUNT(*) FROM agent_update_assignments WHERE agent_id IN (`+placeholders+`) AND (state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback')) GROUP BY agent_id`, arguments...)
	if err != nil {
		return nil, publicDBError(err)
	}
	for rows.Next() {
		var id, count int64
		if err := rows.Scan(&id, &count); err != nil {
			rows.Close()
			return nil, publicDBError(err)
		}
		activeAssignments[id] = count
	}
	if err := rows.Close(); err != nil {
		return nil, publicDBError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, publicDBError(err)
	}
	routeBlockers := a.agentUpdateRouteBlockersForAgents(ids, minimum)
	result := make([]*p2pstreamv1.AgentUpdatePreviewAgent, 0, len(ids))
	for _, id := range ids {
		row := agents[id]
		item := p2pstreamv1.AgentUpdatePreviewAgent{AgentId: row.id, AgentPublicId: row.publicID, Name: row.name}
		enabled, enrolled := row.enabled, row.enrolled
		item.Enrolled = enrolled == 1
		if enabled != 1 {
			item.Blockers = append(item.Blockers, "agent_disabled")
		}
		if enrolled != 1 {
			item.Blockers = append(item.Blockers, "updater_not_enrolled")
		} else {
			if !row.updaterLastSeen.Valid || time.Since(row.updaterLastSeen.Time) > agentUpdateWorkerFreshness {
				item.Blockers = append(item.Blockers, "updater_not_recently_seen")
			}
			if artifactForPlatform(target.Artifacts, row.updaterOS, row.updaterArch) == nil {
				item.Blockers = append(item.Blockers, "platform_artifact_unavailable")
			}
			if !semver.IsValid(row.updaterVersion) || semver.Compare(row.updaterVersion, target.MinimumUpdaterVersion) < 0 {
				item.Blockers = append(item.Blockers, "updater_version_incompatible")
			}
		}
		if a.AgentHub == nil || a.AgentHub.connectedByID(id) == nil {
			item.Blockers = append(item.Blockers, "agent_disconnected")
		}
		if activeAssignments[id] > 0 {
			item.Blockers = append(item.Blockers, "active_assignment")
		}
		item.Blockers = append(item.Blockers, routeBlockers[id]...)
		item.Eligible = len(item.Blockers) == 0
		result = append(result, &item)
	}
	return result, nil
}

func (a *App) agentUpdateRouteBlockers(agentID, minimum int64) []string {
	if a.agentUpdateRouteBlockersHook != nil {
		return a.agentUpdateRouteBlockersHook(agentID, minimum)
	}
	return a.agentUpdateRouteBlockersForAgents([]int64{agentID}, minimum)[agentID]
}

func (a *App) agentUpdateRouteBlockersForAgents(agentIDs []int64, minimum int64) map[int64][]string {
	blockers := make(map[int64][]string, len(agentIDs))
	snap := a.currentPublicSnapshot()
	if snap == nil {
		return blockers
	}
	requested := make(map[int64]struct{}, len(agentIDs))
	for _, id := range agentIDs {
		requested[id] = struct{}{}
	}
	for _, target := range snap.RouteTargets {
		if target.Transport != publicRouteTargetTransportAgent || !target.Enabled {
			continue
		}
		eligible := make(map[int64]struct{})
		for otherID, other := range snap.Agents {
			if !other.Enabled || a.isAgentUpdateCordoned(otherID) || !agentSelectorMatchesLabels(target.AgentSelector, other.Labels) {
				continue
			}
			if a.AgentHub == nil || a.AgentHub.connectedByID(otherID) == nil {
				continue
			}
			if a.TargetHealth != nil && !a.TargetHealth.agentAvailable(target.ID, otherID) {
				continue
			}
			eligible[otherID] = struct{}{}
		}
		for agentID := range requested {
			cfg, ok := snap.Agents[agentID]
			if !ok || !cfg.Enabled || !agentSelectorMatchesLabels(target.AgentSelector, cfg.Labels) {
				continue
			}
			otherEligible := int64(len(eligible))
			if _, candidateIsEligible := eligible[agentID]; candidateIsEligible {
				otherEligible--
			}
			if otherEligible < minimum {
				blockers[agentID] = append(blockers[agentID], fmt.Sprintf("route_target_%d_requires_%d_other_eligible_agents", target.ID, minimum))
			}
		}
	}
	return blockers
}

func (a *App) verifyAgentUpdaterRequest(ctx context.Context, publicID string, counter uint64, signature, payload []byte) (agentUpdaterIdentityRow, error) {
	row, err := authenticateAgentUpdaterRequest(ctx, a.DB, publicID, counter, signature, payload)
	if err != nil {
		return agentUpdaterIdentityRow{}, err
	}
	if err := consumeAgentUpdaterCounter(ctx, a.DB, row.AgentID, counter, time.Now().UTC()); err != nil {
		return agentUpdaterIdentityRow{}, err
	}
	row.LastCounter = int64(counter)
	return row, nil
}

func authenticateAgentUpdaterRequest(ctx context.Context, query db.DBTX, publicID string, counter uint64, signature, payload []byte) (agentUpdaterIdentityRow, error) {
	if strings.TrimSpace(publicID) == "" || counter == 0 || counter > math.MaxInt64 || len(signature) != ed25519.SignatureSize {
		return agentUpdaterIdentityRow{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid updater authentication"))
	}
	var row agentUpdaterIdentityRow
	err := query.QueryRowContext(ctx, `SELECT i.agent_id,i.updater_key_id,i.updater_public_key,i.activator_key_id,i.activator_public_key,i.os,i.arch,i.updater_version,i.trusted_root_sha256,i.trusted_root_version,i.pinned_repository,i.authority_key_id,i.authority_epoch,i.enrollment_generation,i.enrollment_receipt_payload,i.enrollment_receipt_signature,i.enabled,i.last_counter,i.last_command_sequence,i.last_root_action_counter,i.enrolled_at,i.last_seen_at,i.updated_at,a.public_id FROM agent_updater_identities i JOIN agents a ON a.id=i.agent_id WHERE a.public_id=? AND a.enabled=1 AND i.enabled=1`, publicID).Scan(&row.AgentID, &row.UpdaterKeyID, &row.UpdaterPublicKey, &row.ActivatorKeyID, &row.ActivatorPublicKey, &row.Os, &row.Arch, &row.UpdaterVersion, &row.TrustedRootSha256, &row.TrustedRootVersion, &row.PinnedRepository, &row.AuthorityKeyID, &row.AuthorityEpoch, &row.EnrollmentGeneration, &row.EnrollmentReceiptPayload, &row.EnrollmentReceiptSignature, &row.Enabled, &row.LastCounter, &row.LastCommandSequence, &row.LastRootActionCounter, &row.EnrolledAt, &row.LastSeenAt, &row.UpdatedAt, &row.AgentPublicID)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(row.UpdaterPublicKey), payload, signature) {
		return agentUpdaterIdentityRow{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid updater authentication"))
	}
	return row, nil
}

func consumeAgentUpdaterCounter(ctx context.Context, query db.DBTX, agentID int64, counter uint64, now time.Time) error {
	result, err := query.ExecContext(ctx, `UPDATE agent_updater_identities SET last_counter=?,last_seen_at=?,updated_at=? WHERE agent_id=? AND enabled=1 AND last_counter<?`, int64(counter), now, now, agentID, int64(counter))
	if err != nil {
		return publicDBError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("updater counter was replayed"))
	}
	return nil
}

// Signing payloads are deterministic length-prefixed binary records. The
// domain and method are included to prevent cross-endpoint signature reuse.
func agentUpdaterCheckSigningPayload(m *p2pstreamv1.CheckAgentUpdateRequest) []byte {
	return agentupdateauth.CheckPayload(m.AgentPublicId, m.Counter)
}

func agentUpdaterReportSigningPayload(m *p2pstreamv1.ReportAgentUpdateRequest) []byte {
	return agentupdateauth.ReportPayload(agentupdateauth.Report{AgentPublicID: m.AgentPublicId, Counter: m.Counter, AssignmentID: m.AssignmentId, Generation: m.Generation, State: int32(m.State), ManifestSHA256: m.ManifestSha256, BinarySHA256: m.BinarySha256, RunningVersion: m.RunningVersion, RunningCommit: m.RunningCommit, FailureCode: m.FailureCode, FailureDetail: m.FailureDetail, ActivationCounter: m.ActivationCounter, ActivationNonce: m.ActivationNonce, ActivatorSignature: m.ActivatorSignature})
}

func (a *App) activeAgentUpdateAssignment(ctx context.Context, agentID int64) (agentUpdateAssignmentRow, agentUpdateCampaignRow, error) {
	return activeAgentUpdateAssignmentQuery(ctx, a.DB, agentID)
}

func activeAgentUpdateAssignmentQuery(ctx context.Context, query db.DBTX, agentID int64) (agentUpdateAssignmentRow, agentUpdateCampaignRow, error) {
	rows, err := query.QueryContext(ctx, `SELECT x.id,x.campaign_id,x.agent_id,x.state,x.desired_action,x.generation,x.cordoned,x.failure_code,x.failure_detail,x.attested_manifest_sha256,x.attested_binary_sha256,x.attested_activation_counter,x.activation_nonce_hash,x.authorization_action,x.authorization_server_version,x.command_sequence,x.authorization_nonce,x.authorization_sha256,x.authorization_payload,x.authorization_signature,x.authorization_issued_at,x.authorization_expires_at,x.root_action_counter,x.root_action_receipt_payload,x.root_action_receipt_signature,x.root_action_completed_at,x.root_result_kind,x.root_result_root_version,x.root_result_manifest_sha256,x.root_result_version,x.root_result_commit,x.root_result_release_sequence,x.root_result_security_epoch,x.root_result_os,x.root_result_arch,x.root_result_artifact_name,x.root_result_artifact_size,x.root_result_artifact_sha256,x.running_version,x.running_commit,x.observed_version,x.observed_commit,x.activated_at,x.fresh_tunnel_at,x.healthy_at,x.last_report_at,x.created_at,x.updated_at,a.public_id,a.name,c.id,c.name,c.state,c.generation,c.target_version,c.target_commit,c.manifest_sha256,c.release_sequence,c.root_version,c.security_epoch,c.minimum_updater_version,c.minimum_tunnel_protocol,c.maximum_tunnel_protocol,c.artifacts_json,c.max_unavailable,c.minimum_eligible_agents_per_route,c.canary_count,c.wave_size,c.healthy_dwell_millis,c.created_by_user_id,c.created_at,c.updated_at,c.completed_at FROM agent_update_assignments x JOIN agents a ON a.id=x.agent_id JOIN agent_update_campaigns c ON c.id=x.campaign_id WHERE x.agent_id=? AND (x.state NOT IN ('succeeded','failed','cancelled') OR (x.state='failed' AND x.desired_action='rollback')) ORDER BY x.id DESC LIMIT 2`, agentID)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, sql.ErrNoRows
	}
	assignment, campaign, err := scanAgentUpdateJoined(rows)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, err
	}
	if rows.Next() {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, errors.New("agent has multiple active update assignments")
	}
	return assignment, campaign, nil
}

type rowScanner interface{ Scan(...any) error }

func scanAgentUpdateJoined(row rowScanner) (agentUpdateAssignmentRow, agentUpdateCampaignRow, error) {
	var x agentUpdateAssignmentRow
	var c agentUpdateCampaignRow
	destinations := agentUpdateAssignmentScanDestinations(&x)
	destinations = append(destinations, &x.AgentPublicID, &x.AgentName,
		&c.ID, &c.Name, &c.State, &c.Generation, &c.TargetVersion, &c.TargetCommit,
		&c.ManifestSha256, &c.ReleaseSequence, &c.RootVersion, &c.SecurityEpoch,
		&c.MinimumUpdaterVersion, &c.MinimumTunnelProtocol, &c.MaximumTunnelProtocol,
		&c.ArtifactsJson, &c.MaxUnavailable, &c.MinimumEligibleAgentsPerRoute,
		&c.CanaryCount, &c.WaveSize, &c.HealthyDwellMillis, &c.CreatedByUserID,
		&c.CreatedAt, &c.UpdatedAt, &c.CompletedAt)
	err := row.Scan(destinations...)
	if err != nil {
		return x, c, err
	}
	if err := json.Unmarshal([]byte(c.ArtifactsJson), &c.Artifacts); err != nil {
		return x, c, err
	}
	return x, c, nil
}

func agentUpdateAssignmentScanDestinations(x *agentUpdateAssignmentRow) []any {
	return []any{
		&x.ID, &x.CampaignID, &x.AgentID, &x.State, &x.DesiredAction, &x.Generation,
		&x.Cordoned, &x.FailureCode, &x.FailureDetail, &x.AttestedManifestSha256,
		&x.AttestedBinarySha256, &x.AttestedActivationCounter, &x.ActivationNonceHash,
		&x.AuthorizationAction, &x.AuthorizationServerVersion, &x.CommandSequence,
		&x.AuthorizationNonce, &x.AuthorizationSha256, &x.AuthorizationPayload,
		&x.AuthorizationSignature, &x.AuthorizationIssuedAt, &x.AuthorizationExpiresAt,
		&x.RootActionCounter, &x.RootActionReceiptPayload, &x.RootActionReceiptSignature,
		&x.RootActionCompletedAt, &x.RootResultKind, &x.RootResultRootVersion,
		&x.RootResultManifestSha256, &x.RootResultVersion, &x.RootResultCommit,
		&x.RootResultReleaseSequence, &x.RootResultSecurityEpoch, &x.RootResultOs,
		&x.RootResultArch, &x.RootResultArtifactName, &x.RootResultArtifactSize,
		&x.RootResultArtifactSha256, &x.RunningVersion, &x.RunningCommit,
		&x.ObservedVersion, &x.ObservedCommit, &x.ActivatedAt, &x.FreshTunnelAt,
		&x.HealthyAt, &x.LastReportAt, &x.CreatedAt, &x.UpdatedAt,
	}
}

func (a *App) getAgentUpdateCampaignProto(ctx context.Context, id int64) (*p2pstreamv1.AgentUpdateCampaign, error) {
	var c agentUpdateCampaignRow
	err := a.DB.QueryRowContext(ctx, `SELECT id,name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_by_user_id,created_at,updated_at,completed_at FROM agent_update_campaigns WHERE id=?`, id).Scan(&c.ID, &c.Name, &c.State, &c.Generation, &c.TargetVersion, &c.TargetCommit, &c.ManifestSha256, &c.ReleaseSequence, &c.RootVersion, &c.SecurityEpoch, &c.MinimumUpdaterVersion, &c.MinimumTunnelProtocol, &c.MaximumTunnelProtocol, &c.ArtifactsJson, &c.MaxUnavailable, &c.MinimumEligibleAgentsPerRoute, &c.CanaryCount, &c.WaveSize, &c.HealthyDwellMillis, &c.CreatedByUserID, &c.CreatedAt, &c.UpdatedAt, &c.CompletedAt)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := json.Unmarshal([]byte(c.ArtifactsJson), &c.Artifacts); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	result := &p2pstreamv1.AgentUpdateCampaign{Id: c.ID, Name: c.Name, State: campaignStateProto(c.State), Generation: c.Generation, Target: campaignTargetProto(c), Policy: &p2pstreamv1.AgentUpdatePolicy{MaxUnavailable: c.MaxUnavailable, MinimumEligibleAgentsPerRoute: c.MinimumEligibleAgentsPerRoute, CanaryCount: c.CanaryCount, WaveSize: c.WaveSize, HealthyDwellMillis: c.HealthyDwellMillis}, CreatedAtUnixMillis: c.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: c.UpdatedAt.UnixMilli()}
	rows, err := a.DB.QueryContext(ctx, `SELECT x.id,x.campaign_id,x.agent_id,x.state,x.desired_action,x.generation,x.cordoned,x.failure_code,x.failure_detail,x.attested_manifest_sha256,x.attested_binary_sha256,x.attested_activation_counter,x.activation_nonce_hash,x.authorization_action,x.authorization_server_version,x.command_sequence,x.authorization_nonce,x.authorization_sha256,x.authorization_payload,x.authorization_signature,x.authorization_issued_at,x.authorization_expires_at,x.root_action_counter,x.root_action_receipt_payload,x.root_action_receipt_signature,x.root_action_completed_at,x.root_result_kind,x.root_result_root_version,x.root_result_manifest_sha256,x.root_result_version,x.root_result_commit,x.root_result_release_sequence,x.root_result_security_epoch,x.root_result_os,x.root_result_arch,x.root_result_artifact_name,x.root_result_artifact_size,x.root_result_artifact_sha256,x.running_version,x.running_commit,x.observed_version,x.observed_commit,x.activated_at,x.fresh_tunnel_at,x.healthy_at,x.last_report_at,x.created_at,x.updated_at,a.public_id,a.name FROM agent_update_assignments x JOIN agents a ON a.id=x.agent_id WHERE x.campaign_id=? ORDER BY x.id`, id)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var x agentUpdateAssignmentRow
		destinations := agentUpdateAssignmentScanDestinations(&x)
		destinations = append(destinations, &x.AgentPublicID, &x.AgentName)
		if err := rows.Scan(destinations...); err != nil {
			return nil, publicDBError(err)
		}
		result.Assignments = append(result.Assignments, assignmentProto(x))
	}
	return result, rows.Err()
}

func campaignTargetProto(c agentUpdateCampaignRow) *p2pstreamv1.AgentUpdateTarget {
	return &p2pstreamv1.AgentUpdateTarget{Version: c.TargetVersion, Commit: c.TargetCommit, ManifestSha256: c.ManifestSha256, ReleaseSequence: c.ReleaseSequence, MinimumTunnelProtocol: c.MinimumTunnelProtocol, MaximumTunnelProtocol: c.MaximumTunnelProtocol, Artifacts: c.Artifacts, SecurityEpoch: c.SecurityEpoch, MinimumUpdaterVersion: c.MinimumUpdaterVersion, RootVersion: c.RootVersion}
}
func assignmentProto(x agentUpdateAssignmentRow) *p2pstreamv1.AgentUpdateAssignment {
	return &p2pstreamv1.AgentUpdateAssignment{Id: x.ID, CampaignId: x.CampaignID, AgentId: x.AgentID, AgentPublicId: x.AgentPublicID, AgentName: x.AgentName, State: assignmentStateProto(x.State), DesiredAction: desiredActionProto(x.DesiredAction), Generation: x.Generation, Cordoned: x.Cordoned == 1, FailureCode: x.FailureCode, FailureDetail: x.FailureDetail, AttestedManifestSha256: x.AttestedManifestSha256, AttestedBinarySha256: x.AttestedBinarySha256, ActivatedAtUnixMillis: nullTimeMillis(x.ActivatedAt), FreshTunnelAtUnixMillis: nullTimeMillis(x.FreshTunnelAt), HealthyAtUnixMillis: nullTimeMillis(x.HealthyAt), UpdatedAtUnixMillis: x.UpdatedAt.UnixMilli(), ObservedVersion: x.ObservedVersion, ObservedCommit: x.ObservedCommit}
}
func nullTimeMillis(t sql.NullTime) int64 {
	if t.Valid {
		return t.Time.UnixMilli()
	}
	return 0
}

func (a *App) tryAdvanceAgentUpdateAssignment(ctx context.Context, x agentUpdateAssignmentRow, c agentUpdateCampaignRow) {
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	// Check/Report load state before acquiring the shared campaign mutex. An
	// administrator may pause or cancel in that interval, so never use those
	// snapshots to publish a routing fence. Reload under the lock and require the
	// same assignment generation before advancing.
	current, campaign, err := activeAgentUpdateAssignmentQuery(ctx, a.DB, x.AgentID)
	if err != nil || current.ID != x.ID || current.Generation != x.Generation || campaign.ID != c.ID {
		return
	}
	a.tryAdvanceAgentUpdateAssignmentLocked(ctx, current, campaign)
}
func (a *App) tryAdvanceAgentUpdateAssignmentLocked(ctx context.Context, x agentUpdateAssignmentRow, c agentUpdateCampaignRow) {
	if c.State != "running" {
		return
	}
	if x.State == "cordoned" && x.Cordoned == 1 && x.DesiredAction == "none" {
		a.tryAuthorizeDrainedAgentUpdateAssignmentLocked(ctx, x, c)
		return
	}
	if x.State != "staged" || len(a.agentUpdateRouteBlockers(x.AgentID, c.MinimumEligibleAgentsPerRoute)) > 0 {
		return
	}
	cohortSize, eligible, failed := agentUpdateCohortEligibleQuery(ctx, a.DB, x, c)
	if failed {
		now := time.Now().UTC()
		_, _ = a.DB.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, x.CampaignID)
		return
	}
	if !eligible {
		return
	}
	var unavailable int64
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_update_assignments WHERE campaign_id=? AND (cordoned=1 OR state IN ('activating','awaiting_tunnel','healthy_dwell'))`, x.CampaignID).Scan(&unavailable); err != nil {
		return
	}
	inFlightLimit := c.MaxUnavailable
	if cohortSize < inFlightLimit {
		inFlightLimit = cohortSize
	}
	if unavailable >= inFlightLimit {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Publish the routing fence before the durable transition so no new public
	// stream can enter while the authorization is signed and committed. Roll it
	// back on any transaction failure.
	a.setAgentUpdateCordon(x.AgentID)
	rollbackFence := true
	defer func() {
		if rollbackFence {
			a.clearAgentUpdateCordon(x.AgentID)
		}
	}()
	result, err := a.DB.ExecContext(ctx, `UPDATE agent_update_assignments SET state='cordoned',desired_action='none',cordoned=1,authorization_action='',authorization_server_version='',command_sequence=0,authorization_nonce=X'',authorization_sha256='',authorization_payload=X'',authorization_signature=X'',authorization_issued_at=NULL,authorization_expires_at=NULL,updated_at=? WHERE id=? AND generation=? AND state='staged' AND cordoned=0 AND EXISTS (SELECT 1 FROM agent_update_campaigns WHERE id=? AND state='running')`, now, x.ID, x.Generation, x.CampaignID)
	if err != nil {
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return
	}
	rollbackFence = false
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgent(x.AgentID)
	}
	a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, x.AgentID, "draining", "waiting for active requests to finish")
	x.State, x.DesiredAction, x.Cordoned, x.UpdatedAt = "cordoned", "none", 1, now
	a.tryAuthorizeDrainedAgentUpdateAssignmentLocked(ctx, x, c)
}

func (a *App) tryAuthorizeDrainedAgentUpdateAssignmentLocked(ctx context.Context, x agentUpdateAssignmentRow, c agentUpdateCampaignRow) {
	if x.State != "cordoned" || x.Cordoned != 1 || x.DesiredAction != "none" || c.State != "running" {
		return
	}
	// The deadline bounds the entire reversible pre-authorization phase, not
	// only active request draining. Authority/key/DB failures after drain must
	// not leave the known-good tunnel fenced forever.
	if !x.UpdatedAt.IsZero() && !time.Now().UTC().Before(x.UpdatedAt.Add(agentUpdateDrainTimeout)) {
		a.failAgentUpdateDrainTimeoutLocked(ctx, x)
		return
	}
	// Retire idle keep-alives on every poll. This also makes a process restart in
	// the middle of the durable drain self-healing: startup reloads the cordon,
	// and the next authenticated Check retires pools that predate the restart.
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgent(x.AgentID)
	}
	if !a.agentUpdateAgentDrained(x.AgentID) {
		return
	}
	if blockers := a.agentUpdateRouteBlockers(x.AgentID, c.MinimumEligibleAgentsPerRoute); len(blockers) > 0 {
		a.unwindAgentUpdateDrainForRouteQuorumLocked(ctx, x, blockers)
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	identity, err := agentUpdaterIdentityByAgentID(ctx, tx, x.AgentID)
	if err != nil {
		return
	}
	authorization, err := a.issueAssignmentAuthorizationTx(ctx, tx, identity, x, c, "activate", now)
	if err != nil {
		return
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET desired_action='activate',authorization_action='activate',authorization_server_version=?,command_sequence=?,authorization_nonce=?,authorization_sha256=?,authorization_payload=?,authorization_signature=?,authorization_issued_at=?,authorization_expires_at=?,root_action_counter=0,root_action_receipt_payload=X'',root_action_receipt_signature=X'',root_action_completed_at=NULL,root_result_kind='',root_result_root_version=0,root_result_manifest_sha256='',root_result_version='',root_result_commit='',root_result_release_sequence=0,root_result_security_epoch=0,root_result_os='',root_result_arch='',root_result_artifact_name='',root_result_artifact_size=0,root_result_artifact_sha256='',fresh_tunnel_at=NULL,healthy_at=NULL,updated_at=? WHERE id=? AND generation=? AND state='cordoned' AND desired_action='none' AND cordoned=1 AND EXISTS (SELECT 1 FROM agent_update_campaigns WHERE id=? AND state='running')`, authorization.Value.ServerVersion, int64(authorization.Value.CommandSequence), authorization.Value.Nonce, authorization.SHA256, authorization.Payload, authorization.Signature, time.UnixMilli(authorization.Value.IssuedAtUnixMillis), time.UnixMilli(authorization.Value.ExpiresAtUnixMillis), now, x.ID, x.Generation, x.CampaignID)
	if err != nil {
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, x.AgentID, "activation_authorized", "drain complete")
}

func (a *App) unwindAgentUpdateDrainForRouteQuorumLocked(ctx context.Context, x agentUpdateAssignmentRow, blockers []string) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	detail := boundedString(strings.Join(blockers, ","), agentUpdateMaxFailureDetail)
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='staged',desired_action='none',cordoned=0,failure_code='route_quorum_lost_during_drain',failure_detail=?,updated_at=? WHERE id=? AND generation=? AND state='cordoned' AND desired_action='none' AND authorization_action='' AND cordoned=1`, detail, now, x.ID, x.Generation)
	if err != nil {
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, x.CampaignID); err != nil {
		return
	}
	if tx.Commit() != nil {
		return
	}
	a.clearAgentUpdateCordon(x.AgentID)
	a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, x.AgentID, "route_quorum_lost_during_drain", detail)
}

func (a *App) failAgentUpdateDrainTimeoutLocked(ctx context.Context, x agentUpdateAssignmentRow) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='blocked',desired_action='none',cordoned=0,failure_code='drain_timeout',failure_detail='active requests did not drain before the managed-update deadline',updated_at=? WHERE id=? AND generation=? AND state='cordoned' AND desired_action='none' AND cordoned=1`, now, x.ID, x.Generation)
	if err != nil {
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, x.CampaignID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	a.clearAgentUpdateCordon(x.AgentID)
	a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, x.AgentID, "drain_timeout", "campaign paused; administrator retry required")
}

func (a *App) agentUpdateAgentDrained(agentID int64) bool {
	if a == nil || agentID <= 0 {
		return false
	}
	if a.agentUpdateDrainReady != nil {
		return a.agentUpdateDrainReady(agentID)
	}
	if a.AgentHub == nil {
		return false
	}
	agent := a.AgentHub.connectedByID(agentID)
	if agent == nil || agent.ActiveRequests.Load() != 0 {
		return false
	}
	if a.AgentTransports != nil && a.AgentTransports.inFlightForAgent(agentID) != 0 {
		return false
	}
	// The capacity ledger covers transports that do not live in the reusable
	// pool at all (environment one-shots, health checks, streams opening or
	// waiting for FIN). Require the current session's complete physical lifetime
	// count to reach zero before root receives an activation command.
	if a.agentStreamCapacity != nil && agent.Session != nil {
		sessionKey := agentStreamCapacitySessionKey(agent, agent.Session)
		if a.agentStreamCapacity.snapshot().TotalBySession[sessionKey] != 0 {
			return false
		}
	}
	return true
}

// agentUpdateCohortEligibleLocked enforces stable, discrete rollout cohorts.
// Assignment IDs are allocated in the normalized campaign agent order and are
// immutable, so they form the durable rollout ordinal without another mutable
// scheduler cursor. Every canary must succeed before the first regular wave,
// and every complete regular wave must succeed before the next begins.
func agentUpdateCohortEligibleQuery(ctx context.Context, query db.DBTX, x agentUpdateAssignmentRow, c agentUpdateCampaignRow) (int64, bool, bool) {
	rows, err := query.QueryContext(ctx, `SELECT id,state FROM agent_update_assignments WHERE campaign_id=? ORDER BY id`, x.CampaignID)
	if err != nil {
		return 0, false, false
	}
	defer rows.Close()
	type cohortAssignment struct {
		id    int64
		state string
	}
	assignments := make([]cohortAssignment, 0)
	candidate := -1
	for rows.Next() {
		var item cohortAssignment
		if err := rows.Scan(&item.id, &item.state); err != nil {
			return 0, false, false
		}
		if item.id == x.ID {
			candidate = len(assignments)
		}
		assignments = append(assignments, item)
	}
	if rows.Err() != nil || candidate < 0 || len(assignments) == 0 {
		return 0, false, false
	}
	canaryCount := int(c.CanaryCount)
	if canaryCount < 1 {
		canaryCount = 1
	}
	if canaryCount > len(assignments) {
		canaryCount = len(assignments)
	}
	waveSize := int(c.WaveSize)
	if waveSize < 1 {
		waveSize = 1
	}
	cohortStart, cohortEnd := 0, canaryCount
	if candidate >= canaryCount {
		cohortStart = canaryCount + ((candidate-canaryCount)/waveSize)*waveSize
		cohortEnd = cohortStart + waveSize
		if cohortEnd > len(assignments) {
			cohortEnd = len(assignments)
		}
	}
	for i := 0; i < cohortStart; i++ {
		if assignments[i].state != "succeeded" {
			terminalFailure := assignments[i].state == "failed" || assignments[i].state == "blocked" || assignments[i].state == "cancelled"
			return int64(cohortEnd - cohortStart), false, terminalFailure
		}
	}
	for i := cohortStart; i < cohortEnd; i++ {
		if assignments[i].state == "failed" || assignments[i].state == "blocked" || assignments[i].state == "cancelled" {
			return int64(cohortEnd - cohortStart), false, true
		}
		if assignments[i].state == "pending" || assignments[i].state == "staging" {
			return int64(cohortEnd - cohortStart), false, false
		}
	}
	return int64(cohortEnd - cohortStart), true, false
}

func (a *App) appendAgentUpdateEvent(ctx context.Context, campaignID, assignmentID, agentID int64, kind, detail string) {
	if len(kind) > 64 {
		kind = kind[:64]
	}
	detail = boundedString(detail, agentUpdateMaxFailureDetail)
	_, _ = a.DB.ExecContext(ctx, `INSERT INTO agent_update_events (campaign_id,assignment_id,agent_id,kind,detail,created_at) VALUES (?,?,?,?,?,?)`, campaignID, sql.NullInt64{Int64: assignmentID, Valid: assignmentID > 0}, sql.NullInt64{Int64: agentID, Valid: agentID > 0}, kind, detail, time.Now().UTC())
	if assignmentID > 0 {
		_, _ = a.DB.ExecContext(ctx, `DELETE FROM agent_update_events WHERE assignment_id=? AND id NOT IN (SELECT id FROM agent_update_events WHERE assignment_id=? ORDER BY id DESC LIMIT ?)`, assignmentID, assignmentID, agentUpdateEventLimit)
	}
}

func (a *App) loadAgentUpdateCordons(ctx context.Context) error {
	return a.reloadAgentUpdateCordons(ctx)
}
func (a *App) reloadAgentUpdateCordons(ctx context.Context) error {
	if a == nil || a.DB == nil {
		return nil
	}
	next := map[int64]struct{}{}
	rows, err := a.DB.QueryContext(ctx, `SELECT DISTINCT agent_id FROM agent_update_assignments WHERE cordoned=1 AND (state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback'))`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		next[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	a.agentUpdateTrafficMu.Lock()
	a.agentUpdateCordoned.Store(&next)
	a.agentUpdateTrafficMu.Unlock()
	return nil
}
func (a *App) isAgentUpdateCordoned(agentID int64) bool {
	if a == nil || agentID <= 0 {
		return false
	}
	current := a.agentUpdateCordoned.Load()
	if current == nil {
		return false
	}
	_, ok := (*current)[agentID]
	return ok
}
func (a *App) setAgentUpdateCordon(agentID int64) {
	if a == nil || agentID <= 0 {
		return
	}
	a.agentUpdateTrafficMu.Lock()
	defer a.agentUpdateTrafficMu.Unlock()
	next := make(map[int64]struct{})
	if current := a.agentUpdateCordoned.Load(); current != nil {
		for id := range *current {
			next[id] = struct{}{}
		}
	}
	next[agentID] = struct{}{}
	a.agentUpdateCordoned.Store(&next)
}
func (a *App) clearAgentUpdateCordon(agentID int64) {
	if a == nil || agentID <= 0 {
		return
	}
	a.agentUpdateTrafficMu.Lock()
	defer a.agentUpdateTrafficMu.Unlock()
	next := make(map[int64]struct{})
	if current := a.agentUpdateCordoned.Load(); current != nil {
		for id := range *current {
			if id != agentID {
				next[id] = struct{}{}
			}
		}
	}
	a.agentUpdateCordoned.Store(&next)
}

// beginAgentUpdateProtectedRequest closes the stale-selection race between
// route resolution and the actual RoundTrip. Publishing a cordon takes the
// write side of the same fence: a request either increments ActiveRequests
// first and is drained, or observes the cordon and never touches its body.
func (a *App) beginAgentUpdateProtectedRequest(agent *AgentConn) (func(), bool) {
	if a == nil || agent == nil {
		return func() {}, false
	}
	a.agentUpdateTrafficMu.RLock()
	defer a.agentUpdateTrafficMu.RUnlock()
	if a.isAgentUpdateCordoned(agent.AgentID) {
		return func() {}, false
	}
	agent.ActiveRequests.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() { agent.ActiveRequests.Add(-1) })
	}, true
}

func (a *App) recordAgentUpdateFreshTunnel(conn *AgentConn) {
	if a == nil || a.DB == nil || conn == nil {
		return
	}
	agentID := conn.AgentID
	if !a.isAgentUpdateCordoned(agentID) {
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if a.AgentHub == nil || a.AgentHub.connectedByID(agentID) != conn {
			return
		}
		a.agentUpdatesMu.Lock()
		err := a.persistAgentUpdateFreshTunnelLocked(context.Background(), conn)
		if err == nil {
			a.reconcileAgentUpdateSuccessLocked(context.Background(), agentID)
		}
		a.agentUpdatesMu.Unlock()
		if err == nil {
			return
		}
		log.Warn().Err(err).Int64("agent_id", agentID).Int("attempt", attempt).Msg("Failed to persist fresh managed-update tunnel")
		if attempt < 3 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-conn.Done:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (a *App) persistAgentUpdateFreshTunnelLocked(ctx context.Context, conn *AgentConn) error {
	if a == nil || a.DB == nil || conn == nil || a.AgentHub == nil || a.AgentHub.connectedByID(conn.AgentID) != conn {
		return nil
	}
	exec := a.DB.ExecContext
	if a.agentUpdateFreshTunnelWrite != nil {
		exec = a.agentUpdateFreshTunnelWrite
	}
	// A new post-action connection invalidates build evidence from every older
	// session. Repeated observations of the same live connection are no-ops so
	// they cannot keep moving the health-dwell anchor.
	_, err := exec(ctx, `UPDATE agent_update_assignments SET fresh_tunnel_at=?,observed_version=?,observed_commit=?,updated_at=? WHERE agent_id=? AND cordoned=1 AND root_action_completed_at IS NOT NULL AND root_action_completed_at<? AND state IN ('awaiting_tunnel','healthy_dwell') AND (fresh_tunnel_at IS NULL OR fresh_tunnel_at<>?)`, conn.ConnectedAt, conn.BuildVersion, conn.BuildCommit, conn.ConnectedAt, conn.AgentID, conn.ConnectedAt, conn.ConnectedAt)
	return err
}

func (a *App) recordAgentUpdateObservedBuild(agentID int64, _ agentBuildIdentity) {
	if a == nil || a.DB == nil {
		return
	}
	if !a.isAgentUpdateCordoned(agentID) {
		return
	}
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	if conn := a.AgentHub.connectedByID(agentID); conn != nil {
		if err := a.persistAgentUpdateFreshTunnelLocked(context.Background(), conn); err != nil {
			log.Warn().Err(err).Int64("agent_id", agentID).Msg("Failed to persist fresh managed-update tunnel during authenticated build report")
		}
	}
	// Stats are authenticated per agent, not per tunnel generation. They may
	// trigger reconciliation, but only the version/commit captured on the exact
	// authenticated upgrade connection above can become build evidence.
	a.reconcileAgentUpdateSuccessLocked(context.Background(), agentID)
}
func (a *App) reconcileAgentUpdateSuccessLocked(ctx context.Context, agentID int64) {
	x, c, err := a.activeAgentUpdateAssignment(ctx, agentID)
	if err != nil {
		return
	}
	var conn *AgentConn
	if a.AgentHub != nil {
		conn = a.AgentHub.connectedByID(agentID)
	}
	connectedFreshTunnel := conn != nil && x.FreshTunnelAt.Valid && conn.ConnectedAt.Equal(x.FreshTunnelAt.Time)
	liveBuildMatches := conn != nil && conn.BuildVersion == x.RootResultVersion && conn.BuildCommit == x.RootResultCommit
	exactSessionBuildEvidence := liveBuildMatches && x.ObservedVersion == x.RootResultVersion && x.ObservedCommit == x.RootResultCommit
	// Bootstrap rollback may restore an agent released before tunnel build
	// headers existed. In that one compatibility case the root-signed receipt
	// already binds the exact local slot bytes/build, and the server-forced
	// post-receipt reconnect supplies the fresh execution edge. Newly activated
	// signed releases (and rollback to signed releases) still require exact build
	// headers from that same live connection.
	bootstrapRollbackEvidence := x.AuthorizationAction == "rollback" && x.RootResultKind == string(agentupdateauth.RootActionResultBootstrap)
	evidenceComplete := connectedFreshTunnel && x.RootResultKind != "" && (exactSessionBuildEvidence || bootstrapRollbackEvidence)
	requiredEvidenceComplete := evidenceComplete && (x.AuthorizationAction == "rollback" || x.HealthyAt.Valid)
	if x.Cordoned == 1 && x.RootActionCompletedAt.Valid && (x.State == "awaiting_tunnel" || x.State == "healthy_dwell") && !requiredEvidenceComplete && !time.Now().UTC().Before(x.RootActionCompletedAt.Time.Add(agentUpdatePostActionTimeout)) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		failureCode := "activation_evidence_timeout"
		if x.AuthorizationAction == "rollback" {
			failureCode = "rollback_evidence_timeout"
		}
		tx, txErr := a.DB.BeginTx(ctx, nil)
		if txErr != nil {
			return
		}
		defer tx.Rollback()
		result, updateErr := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET state='blocked',desired_action='none',failure_code=?,failure_detail='post-action tunnel and exact build evidence did not arrive before the deadline',updated_at=? WHERE id=? AND generation=? AND cordoned=1 AND state IN ('awaiting_tunnel','healthy_dwell')`, failureCode, now, x.ID, x.Generation)
		if updateErr != nil {
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused',generation=generation+1,updated_at=? WHERE id=? AND state='running'`, now, x.CampaignID); updateErr != nil {
			return
		}
		if tx.Commit() != nil {
			return
		}
		a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, agentID, failureCode, "campaign paused; administrator rollback required")
		return
	}
	if x.AuthorizationAction == "rollback" && x.State == "awaiting_tunnel" && x.Cordoned == 1 && x.RootActionCompletedAt.Valid && evidenceComplete {
		if a.agentUpdateBeforeSuccessCAS != nil {
			a.agentUpdateBeforeSuccessCAS()
		}
		unlockConnection, stillConnected := a.AgentHub.lockCurrentConnection(agentID, conn)
		if !stillConnected {
			return
		}
		now := time.Now().UTC()
		result, updateErr := a.DB.ExecContext(ctx, `UPDATE agent_update_assignments SET state='failed',desired_action='none',cordoned=0,updated_at=? WHERE id=? AND generation=? AND state='awaiting_tunnel' AND authorization_action='rollback' AND cordoned=1`, now, x.ID, x.Generation)
		recovered := false
		if updateErr == nil {
			if count, _ := result.RowsAffected(); count == 1 {
				a.clearAgentUpdateCordon(agentID)
				recovered = true
			}
		}
		unlockConnection()
		if recovered {
			a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, agentID, "rollback_recovered", "")
		}
		return
	}
	dwellStartedAt := x.HealthyAt.Time
	if x.FreshTunnelAt.Valid && x.FreshTunnelAt.Time.After(dwellStartedAt) {
		dwellStartedAt = x.FreshTunnelAt.Time
	}
	// updated_at is advanced when the exact authenticated build observation is
	// stored. Starting dwell after the latest required evidence prevents an old
	// worker HEALTHY report from satisfying dwell immediately after a late
	// reconnect/build report.
	if x.UpdatedAt.After(dwellStartedAt) {
		dwellStartedAt = x.UpdatedAt
	}
	if c.State != "running" || x.DesiredAction != "none" || x.AuthorizationAction != "activate" || x.State != "healthy_dwell" || !x.ActivatedAt.Valid || !x.RootActionCompletedAt.Valid || !x.HealthyAt.Valid || !evidenceComplete || x.RootResultKind != string(agentupdateauth.RootActionResultSignedRelease) || x.RootResultManifestSha256 != c.ManifestSha256 || x.RootResultArtifactSha256 != x.AttestedBinarySha256 || x.RunningVersion != c.TargetVersion || x.RunningCommit != c.TargetCommit || x.ObservedVersion != c.TargetVersion || x.ObservedCommit != c.TargetCommit || time.Since(dwellStartedAt) < time.Duration(c.HealthyDwellMillis)*time.Millisecond {
		return
	}
	if a.agentUpdateBeforeSuccessCAS != nil {
		a.agentUpdateBeforeSuccessCAS()
	}
	unlockConnection, stillConnected := a.AgentHub.lockCurrentConnection(agentID, conn)
	if !stillConnected {
		return
	}
	now := time.Now().UTC()
	result, err := a.DB.ExecContext(ctx, `UPDATE agent_update_assignments SET state='succeeded',desired_action='none',cordoned=0,updated_at=? WHERE id=? AND generation=? AND state='healthy_dwell' AND desired_action='none' AND cordoned=1 AND EXISTS (SELECT 1 FROM agent_update_campaigns WHERE id=? AND state='running')`, now, x.ID, x.Generation, x.CampaignID)
	if err != nil {
		unlockConnection()
		return
	}
	succeeded := false
	if n, _ := result.RowsAffected(); n == 1 {
		a.clearAgentUpdateCordon(agentID)
		succeeded = true
	}
	unlockConnection()
	if succeeded {
		a.appendAgentUpdateEvent(ctx, x.CampaignID, x.ID, agentID, "succeeded", "")
		if releaseErr := a.releaseNextAgentUpdateStageCohortLocked(ctx, x.CampaignID, c); releaseErr != nil {
			log.Warn().Err(releaseErr).Int64("campaign_id", x.CampaignID).Msg("Failed to release next managed-update staging cohort; authenticated checks will retry")
		}
		if err := a.finalizeCompletedAgentUpdateCampaignsLocked(ctx, now); err != nil {
			log.Warn().Err(err).Int64("campaign_id", x.CampaignID).Msg("Failed to finalize managed-update campaign; maintenance will retry")
		}
	}
}

func (a *App) releaseNextAgentUpdateStageCohortLocked(ctx context.Context, campaignID int64, campaign agentUpdateCampaignRow) error {
	if campaign.State != "running" {
		return nil
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&state); err != nil {
		return err
	}
	if state != "running" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,state,desired_action FROM agent_update_assignments WHERE campaign_id=? ORDER BY id`, campaignID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type assignment struct {
		id     int64
		state  string
		action string
	}
	assignments := make([]assignment, 0)
	for rows.Next() {
		var item assignment
		if err := rows.Scan(&item.id, &item.state, &item.action); err != nil {
			return err
		}
		assignments = append(assignments, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	first := 0
	for first < len(assignments) && assignments[first].state == "succeeded" {
		first++
	}
	if first >= len(assignments) {
		return nil
	}
	canaryCount := int(campaign.CanaryCount)
	if canaryCount < 1 {
		canaryCount = 1
	}
	if canaryCount > len(assignments) {
		canaryCount = len(assignments)
	}
	waveSize := int(campaign.WaveSize)
	if waveSize < 1 {
		waveSize = 1
	}
	start, end := 0, canaryCount
	if first >= canaryCount {
		start = canaryCount + ((first-canaryCount)/waveSize)*waveSize
		end = start + waveSize
		if end > len(assignments) {
			end = len(assignments)
		}
	}
	for i := 0; i < start; i++ {
		if assignments[i].state != "succeeded" {
			return nil
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := start; i < end; i++ {
		if assignments[i].state == "pending" && assignments[i].action == "none" {
			result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET desired_action='stage',updated_at=? WHERE id=? AND state='pending' AND desired_action='none'`, now, assignments[i].id)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("release managed-update staging assignment %d: row changed", assignments[i].id)
			}
		}
	}
	return tx.Commit()
}

func artifactForPlatform(artifacts []*p2pstreamv1.AgentUpdateArtifact, osName, arch string) *p2pstreamv1.AgentUpdateArtifact {
	for _, a := range artifacts {
		if a != nil && a.Os == osName && a.Arch == arch {
			return a
		}
	}
	return nil
}
func artifactDigestInCampaign(artifacts []*p2pstreamv1.AgentUpdateArtifact, digest string) bool {
	if !agentUpdateDigestRE.MatchString(digest) {
		return false
	}
	for _, a := range artifacts {
		if a != nil && a.Sha256 == digest {
			return true
		}
	}
	return false
}
func boundedString(value string, max int) string {
	return truncateProxyRequestContextValue(value, max)
}
func chooseString(condition bool, a, b string) string {
	if condition {
		return a
	}
	return b
}
func campaignStateProto(v string) p2pstreamv1.AgentUpdateCampaignState {
	switch v {
	case "running":
		return p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_RUNNING
	case "paused":
		return p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_PAUSED
	case "cancelled":
		return p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_CANCELLED
	case "completed":
		return p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_COMPLETED
	}
	return p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_UNSPECIFIED
}
func assignmentStateProto(v string) p2pstreamv1.AgentUpdateAssignmentState {
	switch v {
	case "pending":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_PENDING
	case "staging":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_STAGING
	case "staged":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_STAGED
	case "cordoned":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_CORDONED
	case "activating":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_ACTIVATING
	case "awaiting_tunnel":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL
	case "healthy_dwell":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_HEALTHY_DWELL
	case "succeeded":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_SUCCEEDED
	case "failed":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_FAILED
	case "cancelled":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_CANCELLED
	case "blocked":
		return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_BLOCKED
	}
	return p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_UNSPECIFIED
}
func desiredActionProto(v string) p2pstreamv1.AgentUpdateDesiredAction {
	switch v {
	case "none":
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE
	case "stage":
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_STAGE
	case "activate":
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE
	case "rollback":
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK
	}
	return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_UNSPECIFIED
}
