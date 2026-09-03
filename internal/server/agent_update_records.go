package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/db"
)

type signedAssignmentAuthorization struct {
	Value     agentupdateauth.AssignmentAuthorization
	Payload   []byte
	Signature []byte
	SHA256    string
}

func agentUpdaterIdentityByAgentID(ctx context.Context, query db.DBTX, agentID int64) (agentUpdaterIdentityRow, error) {
	var row agentUpdaterIdentityRow
	err := query.QueryRowContext(ctx, `SELECT i.agent_id,i.updater_key_id,i.updater_public_key,i.activator_key_id,i.activator_public_key,i.os,i.arch,i.updater_version,i.pinned_repository,i.authority_key_id,i.authority_epoch,i.enrollment_generation,i.enrollment_receipt_payload,i.enrollment_receipt_signature,i.enabled,i.last_counter,i.last_command_sequence,i.last_root_action_counter,i.enrolled_at,i.last_seen_at,i.updated_at,a.public_id FROM agent_updater_identities i JOIN agents a ON a.id=i.agent_id WHERE i.agent_id=? AND a.enabled=1 AND i.enabled=1`, agentID).Scan(&row.AgentID, &row.UpdaterKeyID, &row.UpdaterPublicKey, &row.ActivatorKeyID, &row.ActivatorPublicKey, &row.Os, &row.Arch, &row.UpdaterVersion, &row.PinnedRepository, &row.AuthorityKeyID, &row.AuthorityEpoch, &row.EnrollmentGeneration, &row.EnrollmentReceiptPayload, &row.EnrollmentReceiptSignature, &row.Enabled, &row.LastCounter, &row.LastCommandSequence, &row.LastRootActionCounter, &row.EnrolledAt, &row.LastSeenAt, &row.UpdatedAt, &row.AgentPublicID)
	return row, err
}

func desiredActionAuth(value string) (agentupdateauth.AssignmentAction, error) {
	switch value {
	case "activate":
		return agentupdateauth.AssignmentActionActivate, nil
	case "rollback":
		return agentupdateauth.AssignmentActionRollback, nil
	default:
		return "", errors.New("assignment action is not privileged")
	}
}

func campaignAllowsAgentUpdateAuthorization(campaignState, action string) bool {
	switch action {
	case "activate":
		return campaignState == "running"
	case "rollback":
		// Cancellation deliberately remains able to deliver its fail-closed
		// rollback command. A pause, however, freezes every privileged action.
		return campaignState == "running" || campaignState == "cancelled"
	default:
		return false
	}
}

func desiredActionAuthProto(value agentupdateauth.AssignmentAction) p2pstreamv1.AgentUpdateDesiredAction {
	switch value {
	case agentupdateauth.AssignmentActionActivate:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE
	case agentupdateauth.AssignmentActionRollback:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK
	default:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_UNSPECIFIED
	}
}

func desiredActionAuthFromProto(value p2pstreamv1.AgentUpdateDesiredAction) (agentupdateauth.AssignmentAction, error) {
	switch value {
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE:
		return agentupdateauth.AssignmentActionActivate, nil
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK:
		return agentupdateauth.AssignmentActionRollback, nil
	default:
		return "", errors.New("root action receipt has an invalid action")
	}
}

func (a *App) issueAssignmentAuthorizationTx(ctx context.Context, tx *sql.Tx, identity agentUpdaterIdentityRow, assignment agentUpdateAssignmentRow, campaign agentUpdateCampaignRow, action string, now time.Time) (signedAssignmentAuthorization, error) {
	authority, authorityIdentity, err := a.requireAgentUpdateAuthority()
	if err != nil {
		return signedAssignmentAuthorization{}, err
	}
	if identity.AuthorityKeyID != authorityIdentity.KeyID || identity.AuthorityEpoch <= 0 || uint64(identity.AuthorityEpoch) != authorityIdentity.Epoch {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("updater identity is pinned to a different management authority"))
	}
	authAction, err := desiredActionAuth(action)
	if err != nil {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	artifact := artifactForPlatform(campaign.Artifacts, identity.Os, identity.Arch)
	if artifact == nil {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("campaign has no artifact for the enrolled updater platform"))
	}
	var previous int64
	if err := tx.QueryRowContext(ctx, `SELECT last_command_sequence FROM agent_updater_identities WHERE agent_id=? AND enabled=1`, identity.AgentID).Scan(&previous); err != nil {
		return signedAssignmentAuthorization{}, publicDBError(err)
	}
	if previous < 0 || previous >= math.MaxInt64 {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeResourceExhausted, errors.New("management authorization sequence is exhausted"))
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeInternal, err)
	}
	now = now.UTC().Truncate(time.Millisecond)
	value := agentupdateauth.AssignmentAuthorization{
		AgentPublicID: identity.AgentPublicID, AssignmentID: assignment.ID, CampaignID: assignment.CampaignID,
		Generation: assignment.Generation, Action: authAction, CommandSequence: uint64(previous + 1), Nonce: nonce,
		IssuedAtUnixMillis: now.UnixMilli(), ExpiresAtUnixMillis: now.Add(agentupdateauth.MaxAuthorizationLifetime).UnixMilli(),
		AuthorityKeyID: authorityIdentity.KeyID, AuthorityEpoch: authorityIdentity.Epoch, ServerVersion: buildinfo.Version,
		ManifestSHA256: campaign.ManifestSha256,
		TargetVersion:  campaign.TargetVersion, TargetCommit: campaign.TargetCommit,
		ReleaseSequence: uint64(campaign.ReleaseSequence), SecurityEpoch: uint64(campaign.SecurityEpoch),
		OS: identity.Os, Arch: identity.Arch, ArtifactName: artifact.Name, ArtifactSize: artifact.SizeBytes, ArtifactSHA256: artifact.Sha256,
	}
	payload, err := agentupdateauth.AssignmentAuthorizationPayload(value)
	if err != nil {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	signature, err := authority.Sign(payload)
	if err != nil {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	digest := sha256.Sum256(payload)
	result, err := tx.ExecContext(ctx, `UPDATE agent_updater_identities SET last_command_sequence=?,updated_at=? WHERE agent_id=? AND enabled=1 AND last_command_sequence=? AND authority_key_id=? AND authority_epoch=?`, previous+1, now, identity.AgentID, previous, authorityIdentity.KeyID, int64(authorityIdentity.Epoch))
	if err != nil {
		return signedAssignmentAuthorization{}, publicDBError(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return signedAssignmentAuthorization{}, connect.NewError(connect.CodeAborted, errors.New("management authorization sequence changed"))
	}
	return signedAssignmentAuthorization{Value: value, Payload: payload, Signature: signature, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (a *App) ensureAgentUpdateAssignmentAuthorization(ctx context.Context, agentID, assignmentID, generation int64, action string) (agentUpdateAssignmentRow, agentUpdateCampaignRow, error) {
	a.agentUpdatesMu.Lock()
	defer a.agentUpdatesMu.Unlock()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, publicDBError(err)
	}
	defer tx.Rollback()
	assignment, campaign, err := activeAgentUpdateAssignmentQuery(ctx, tx, agentID)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, publicDBError(err)
	}
	if assignment.ID != assignmentID || assignment.Generation != generation || assignment.DesiredAction != action || (action != "activate" && action != "rollback") {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, connect.NewError(connect.CodeAborted, errors.New("assignment authorization request changed"))
	}
	if !campaignAllowsAgentUpdateAuthorization(campaign.State, action) {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("campaign state does not permit privileged update authorization"))
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if assignment.AuthorizationAction == action && assignment.AuthorizationExpiresAt.Valid && assignment.AuthorizationExpiresAt.Time.After(now) {
		return assignment, campaign, nil
	}
	identity, err := agentUpdaterIdentityByAgentID(ctx, tx, agentID)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, publicDBError(err)
	}
	authorization, err := a.issueAssignmentAuthorizationTx(ctx, tx, identity, assignment, campaign, action, now)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action=?,authorization_server_version=?,command_sequence=?,authorization_nonce=?,authorization_sha256=?,authorization_payload=?,authorization_signature=?,authorization_issued_at=?,authorization_expires_at=?,updated_at=? WHERE id=? AND generation=? AND desired_action=? AND (authorization_action<>? OR authorization_expires_at IS NULL OR authorization_expires_at<=?) AND EXISTS (SELECT 1 FROM agent_update_campaigns WHERE id=? AND state=?)`, action, authorization.Value.ServerVersion, int64(authorization.Value.CommandSequence), authorization.Value.Nonce, authorization.SHA256, authorization.Payload, authorization.Signature, time.UnixMilli(authorization.Value.IssuedAtUnixMillis), time.UnixMilli(authorization.Value.ExpiresAtUnixMillis), now, assignment.ID, assignment.Generation, action, action, now, campaign.ID, campaign.State)
	if err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, publicDBError(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, connect.NewError(connect.CodeAborted, errors.New("assignment authorization changed"))
	}
	if err := tx.Commit(); err != nil {
		return agentUpdateAssignmentRow{}, agentUpdateCampaignRow{}, publicDBError(err)
	}
	assignment.AuthorizationAction = action
	assignment.AuthorizationServerVersion = authorization.Value.ServerVersion
	assignment.CommandSequence = int64(authorization.Value.CommandSequence)
	assignment.AuthorizationNonce = append([]byte(nil), authorization.Value.Nonce...)
	assignment.AuthorizationSha256 = authorization.SHA256
	assignment.AuthorizationPayload = append([]byte(nil), authorization.Payload...)
	assignment.AuthorizationSignature = append([]byte(nil), authorization.Signature...)
	assignment.AuthorizationIssuedAt = sql.NullTime{Time: time.UnixMilli(authorization.Value.IssuedAtUnixMillis), Valid: true}
	assignment.AuthorizationExpiresAt = sql.NullTime{Time: time.UnixMilli(authorization.Value.ExpiresAtUnixMillis), Valid: true}
	return assignment, campaign, nil
}

func assignmentAuthorizationProto(record signedAssignmentAuthorization) *p2pstreamv1.AgentUpdateAssignmentAuthorization {
	v := record.Value
	return &p2pstreamv1.AgentUpdateAssignmentAuthorization{
		AgentPublicId: v.AgentPublicID, AssignmentId: v.AssignmentID, CampaignId: v.CampaignID, Generation: v.Generation,
		Action: desiredActionAuthProto(v.Action), CommandSequence: v.CommandSequence, Nonce: append([]byte(nil), v.Nonce...),
		IssuedAtUnixMillis: v.IssuedAtUnixMillis, ExpiresAtUnixMillis: v.ExpiresAtUnixMillis,
		AuthorityKeyId: v.AuthorityKeyID, AuthorityEpoch: v.AuthorityEpoch, ServerVersion: v.ServerVersion,
		ManifestSha256: v.ManifestSHA256, TargetVersion: v.TargetVersion,
		TargetCommit: v.TargetCommit, ReleaseSequence: v.ReleaseSequence, SecurityEpoch: v.SecurityEpoch,
		Os: v.OS, Arch: v.Arch, ArtifactName: v.ArtifactName, ArtifactSize: v.ArtifactSize,
		ArtifactSha256: v.ArtifactSHA256, CanonicalPayload: append([]byte(nil), record.Payload...), Signature: append([]byte(nil), record.Signature...),
	}
}

func storedAssignmentAuthorization(assignment agentUpdateAssignmentRow, campaign agentUpdateCampaignRow, identity agentUpdaterIdentityRow) (signedAssignmentAuthorization, error) {
	action, err := desiredActionAuth(assignment.AuthorizationAction)
	if err != nil || assignment.CommandSequence <= 0 || !assignment.AuthorizationIssuedAt.Valid || !assignment.AuthorizationExpiresAt.Valid {
		return signedAssignmentAuthorization{}, errors.New("stored assignment authorization is incomplete")
	}
	artifact := artifactForPlatform(campaign.Artifacts, identity.Os, identity.Arch)
	if artifact == nil {
		return signedAssignmentAuthorization{}, errors.New("stored assignment authorization has no platform artifact")
	}
	value := agentupdateauth.AssignmentAuthorization{
		AgentPublicID: identity.AgentPublicID, AssignmentID: assignment.ID, CampaignID: assignment.CampaignID,
		Generation: assignment.Generation, Action: action, CommandSequence: uint64(assignment.CommandSequence),
		Nonce: append([]byte(nil), assignment.AuthorizationNonce...), IssuedAtUnixMillis: assignment.AuthorizationIssuedAt.Time.UnixMilli(),
		ExpiresAtUnixMillis: assignment.AuthorizationExpiresAt.Time.UnixMilli(), AuthorityKeyID: identity.AuthorityKeyID,
		AuthorityEpoch: uint64(identity.AuthorityEpoch), ServerVersion: assignment.AuthorizationServerVersion,
		ManifestSHA256: campaign.ManifestSha256,
		TargetVersion:  campaign.TargetVersion, TargetCommit: campaign.TargetCommit,
		ReleaseSequence: uint64(campaign.ReleaseSequence), SecurityEpoch: uint64(campaign.SecurityEpoch),
		OS: identity.Os, Arch: identity.Arch, ArtifactName: artifact.Name, ArtifactSize: artifact.SizeBytes, ArtifactSHA256: artifact.Sha256,
	}
	payload, err := agentupdateauth.AssignmentAuthorizationPayload(value)
	if err != nil || !bytes.Equal(payload, assignment.AuthorizationPayload) || len(assignment.AuthorizationSignature) != ed25519.SignatureSize {
		return signedAssignmentAuthorization{}, errors.New("stored assignment authorization does not match canonical state")
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	if digestText != assignment.AuthorizationSha256 {
		return signedAssignmentAuthorization{}, errors.New("stored assignment authorization digest does not match")
	}
	return signedAssignmentAuthorization{Value: value, Payload: payload, Signature: append([]byte(nil), assignment.AuthorizationSignature...), SHA256: digestText}, nil
}

func enrollmentReceiptProto(value agentupdateauth.EnrollmentReceipt, payload, signature []byte) *p2pstreamv1.AgentUpdaterEnrollmentReceipt {
	return &p2pstreamv1.AgentUpdaterEnrollmentReceipt{
		AgentPublicId: value.AgentPublicID, UpdaterKeyId: value.UpdaterKeyID,
		UpdaterPublicKeySha256: value.UpdaterPublicKeySHA256, ActivatorKeyId: value.ActivatorKeyID,
		ActivatorPublicKeySha256: value.ActivatorPublicKeySHA256, Os: value.OS, Arch: value.Arch,
		UpdaterVersion: value.UpdaterVersion, PinnedRepository: value.PinnedRepository,
		AuthorityKeyId: value.AuthorityKeyID, AuthorityEpoch: value.AuthorityEpoch,
		EnrolledAtUnixMillis: value.EnrolledAtUnixMillis, ExpiresAtUnixMillis: value.ExpiresAtUnixMillis,
		Generation: value.Generation, CanonicalPayload: append([]byte(nil), payload...), Signature: append([]byte(nil), signature...),
	}
}

func rootActionResultKindFromProto(value p2pstreamv1.AgentUpdateRootActionResultKind) (agentupdateauth.RootActionResultKind, error) {
	switch value {
	case p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_RELEASE:
		return agentupdateauth.RootActionResultRelease, nil
	case p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_BOOTSTRAP:
		return agentupdateauth.RootActionResultBootstrap, nil
	default:
		return "", errors.New("root action receipt has an invalid result kind")
	}
}

func rootActionReceiptFromProto(value *p2pstreamv1.AgentUpdateRootActionReceipt) (agentupdateauth.RootActionReceipt, []byte, []byte, error) {
	if value == nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, errors.New("root action receipt is required")
	}
	action, err := desiredActionAuthFromProto(value.Action)
	if err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, err
	}
	kind, err := rootActionResultKindFromProto(value.ResultKind)
	if err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, err
	}
	receipt := agentupdateauth.RootActionReceipt{
		AgentPublicID: value.AgentPublicId, AssignmentID: value.AssignmentId, CampaignID: value.CampaignId,
		Generation: value.Generation, Action: action, CommandSequence: value.CommandSequence,
		AuthorizationSHA256: value.AuthorizationSha256, AuthorizationNonce: append([]byte(nil), value.AuthorizationNonce...),
		AuthorityKeyID: value.AuthorityKeyId, AuthorityEpoch: value.AuthorityEpoch, ActivatorKeyID: value.ActivatorKeyId,
		RootActionCounter: value.RootActionCounter, CompletedAtUnixMillis: value.CompletedAtUnixMillis, ResultKind: kind,
		ResultManifestSHA256: value.ResultManifestSha256,
		ResultVersion:        value.ResultVersion, ResultCommit: value.ResultCommit,
		ResultReleaseSequence: value.ResultReleaseSequence, ResultSecurityEpoch: value.ResultSecurityEpoch,
		ResultOS: value.ResultOs, ResultArch: value.ResultArch, ResultArtifactName: value.ResultArtifactName,
		ResultArtifactSize: value.ResultArtifactSize, ResultArtifactSHA256: value.ResultArtifactSha256,
	}
	payload, err := agentupdateauth.RootActionReceiptPayload(receipt)
	if err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, err
	}
	if !bytes.Equal(payload, value.CanonicalPayload) || len(value.Signature) != ed25519.SignatureSize {
		return agentupdateauth.RootActionReceipt{}, nil, nil, errors.New("root action receipt canonical bytes or signature are invalid")
	}
	return receipt, append([]byte(nil), payload...), append([]byte(nil), value.Signature...), nil
}

func verifyAgentUpdateRootActionReceipt(identity agentUpdaterIdentityRow, assignment agentUpdateAssignmentRow, campaign agentUpdateCampaignRow, value *p2pstreamv1.AgentUpdateRootActionReceipt, expectedAction agentupdateauth.AssignmentAction, now time.Time) (agentupdateauth.RootActionReceipt, []byte, []byte, error) {
	receipt, payload, signature, err := rootActionReceiptFromProto(value)
	if err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	authorization, err := storedAssignmentAuthorization(assignment, campaign, identity)
	if err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if authorization.Value.Action != expectedAction || receipt.AssignmentID != assignment.ID || receipt.CampaignID != assignment.CampaignID || receipt.Generation != assignment.Generation ||
		receipt.CommandSequence != authorization.Value.CommandSequence || !bytes.Equal(receipt.AuthorizationNonce, authorization.Value.Nonce) || receipt.ActivatorKeyID != identity.ActivatorKeyID {
		return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("root action receipt does not match the active assignment authorization"))
	}
	completedAt := time.UnixMilli(receipt.CompletedAtUnixMillis)
	if completedAt.Before(time.UnixMilli(authorization.Value.IssuedAtUnixMillis)) || completedAt.After(time.UnixMilli(authorization.Value.ExpiresAtUnixMillis)) {
		return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("root action completed outside the authorization lifetime"))
	}
	if err := agentupdateauth.VerifyRootActionReceipt(ed25519.PublicKey(identity.ActivatorPublicKey), receipt, signature, agentupdateauth.RootActionReceiptVerifyPolicy{
		Now: now, ExpectedAgentPublicID: identity.AgentPublicID, ExpectedAction: expectedAction,
		ExpectedAuthorizationSHA256: authorization.SHA256, ExpectedAuthorityKeyID: identity.AuthorityKeyID,
		ExpectedAuthorityEpoch: uint64(identity.AuthorityEpoch), LastRootActionCounter: uint64(identity.LastRootActionCounter),
	}); err != nil {
		return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	artifact := artifactForPlatform(campaign.Artifacts, identity.Os, identity.Arch)
	if expectedAction == agentupdateauth.AssignmentActionActivate {
		if receipt.ResultKind != agentupdateauth.RootActionResultRelease || artifact == nil ||
			receipt.ResultManifestSHA256 != campaign.ManifestSha256 ||
			receipt.ResultVersion != campaign.TargetVersion || receipt.ResultCommit != campaign.TargetCommit ||
			receipt.ResultReleaseSequence != uint64(campaign.ReleaseSequence) || receipt.ResultSecurityEpoch != uint64(campaign.SecurityEpoch) ||
			receipt.ResultOS != identity.Os || receipt.ResultArch != identity.Arch || receipt.ResultArtifactName != artifact.Name ||
			receipt.ResultArtifactSize != artifact.SizeBytes || receipt.ResultArtifactSHA256 != artifact.Sha256 {
			return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("activation receipt result does not match the trusted campaign target"))
		}
	} else {
		if receipt.ResultOS != identity.Os || receipt.ResultArch != identity.Arch {
			return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("rollback receipt result platform does not match the enrolled host"))
		}
		if artifact != nil && receipt.ResultKind == agentupdateauth.RootActionResultRelease &&
			receipt.ResultVersion == campaign.TargetVersion && receipt.ResultCommit == campaign.TargetCommit && receipt.ResultArtifactSHA256 == artifact.Sha256 {
			return agentupdateauth.RootActionReceipt{}, nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("rollback receipt did not move away from the failed campaign target"))
		}
	}
	return receipt, payload, signature, nil
}

func consumeRootActionCounter(ctx context.Context, query db.DBTX, identity agentUpdaterIdentityRow, counter uint64, now time.Time) error {
	if counter == 0 || counter > math.MaxInt64 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("root action counter is invalid"))
	}
	result, err := query.ExecContext(ctx, `UPDATE agent_updater_identities SET last_root_action_counter=?,updated_at=? WHERE agent_id=? AND enabled=1 AND last_root_action_counter<?`, int64(counter), now, identity.AgentID, int64(counter))
	if err != nil {
		return publicDBError(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("root action receipt counter was replayed"))
	}
	return nil
}
