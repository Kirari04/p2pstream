package updater

import (
	"bytes"
	"errors"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
)

type enrollmentReceiptRecord struct {
	Receipt               agentupdateauth.EnrollmentReceipt `json:"receipt"`
	CanonicalPayload      []byte                            `json:"canonical_payload"`
	Signature             []byte                            `json:"signature"`
	FirstCheckAt          string                            `json:"first_signed_check_at"`
	EnrollmentTokenSHA256 string                            `json:"enrollment_token_sha256,omitempty"`
}

type assignmentAuthorizationRecord struct {
	Authorization    agentupdateauth.AssignmentAuthorization `json:"authorization"`
	CanonicalPayload []byte                                  `json:"canonical_payload"`
	Signature        []byte                                  `json:"signature"`
}

type rootActionReceiptRecord struct {
	Receipt          agentupdateauth.RootActionReceipt `json:"receipt"`
	CanonicalPayload []byte                            `json:"canonical_payload"`
	Signature        []byte                            `json:"signature"`
}

func enrollmentReceiptFromProto(message *p2pstreamv1.AgentUpdaterEnrollmentReceipt) (enrollmentReceiptRecord, error) {
	if message == nil {
		return enrollmentReceiptRecord{}, errors.New("management enrollment response has no signed receipt")
	}
	record := enrollmentReceiptRecord{
		Receipt: agentupdateauth.EnrollmentReceipt{
			AgentPublicID: message.AgentPublicId, UpdaterKeyID: message.UpdaterKeyId,
			UpdaterPublicKeySHA256: message.UpdaterPublicKeySha256, ActivatorKeyID: message.ActivatorKeyId,
			ActivatorPublicKeySHA256: message.ActivatorPublicKeySha256, OS: message.Os, Arch: message.Arch,
			UpdaterVersion: message.UpdaterVersion, TrustedRootSHA256: message.TrustedRootSha256,
			TrustedRootVersion: message.TrustedRootVersion, PinnedRepository: message.PinnedRepository,
			AuthorityKeyID: message.AuthorityKeyId, AuthorityEpoch: message.AuthorityEpoch,
			EnrolledAtUnixMillis: message.EnrolledAtUnixMillis, ExpiresAtUnixMillis: message.ExpiresAtUnixMillis,
			Generation: message.Generation,
		},
		CanonicalPayload: append([]byte(nil), message.CanonicalPayload...),
		Signature:        append([]byte(nil), message.Signature...),
	}
	payload, err := agentupdateauth.EnrollmentReceiptPayload(record.Receipt)
	if err != nil {
		return enrollmentReceiptRecord{}, err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return enrollmentReceiptRecord{}, errors.New("management enrollment receipt canonical payload mismatch")
	}
	return record, nil
}

func assignmentAuthorizationFromProto(message *p2pstreamv1.AgentUpdateAssignmentAuthorization) (assignmentAuthorizationRecord, error) {
	if message == nil {
		return assignmentAuthorizationRecord{}, errors.New("management response has no signed root-action authorization")
	}
	action, err := authorizationActionFromProto(message.Action)
	if err != nil {
		return assignmentAuthorizationRecord{}, err
	}
	record := assignmentAuthorizationRecord{
		Authorization: agentupdateauth.AssignmentAuthorization{
			AgentPublicID: message.AgentPublicId, AssignmentID: message.AssignmentId, CampaignID: message.CampaignId,
			Generation: message.Generation, Action: action, CommandSequence: message.CommandSequence,
			Nonce: append([]byte(nil), message.Nonce...), IssuedAtUnixMillis: message.IssuedAtUnixMillis,
			ExpiresAtUnixMillis: message.ExpiresAtUnixMillis, AuthorityKeyID: message.AuthorityKeyId,
			AuthorityEpoch: message.AuthorityEpoch, ServerVersion: message.ServerVersion, RootVersion: message.RootVersion,
			ManifestSHA256: message.ManifestSha256, TargetVersion: message.TargetVersion, TargetCommit: message.TargetCommit,
			ReleaseSequence: message.ReleaseSequence, SecurityEpoch: message.SecurityEpoch, OS: message.Os, Arch: message.Arch,
			ArtifactName: message.ArtifactName, ArtifactSize: message.ArtifactSize, ArtifactSHA256: message.ArtifactSha256,
		},
		CanonicalPayload: append([]byte(nil), message.CanonicalPayload...),
		Signature:        append([]byte(nil), message.Signature...),
	}
	payload, err := agentupdateauth.AssignmentAuthorizationPayload(record.Authorization)
	if err != nil {
		return assignmentAuthorizationRecord{}, err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return assignmentAuthorizationRecord{}, errors.New("management assignment authorization canonical payload mismatch")
	}
	return record, nil
}

func rootActionReceiptToProto(record rootActionReceiptRecord) (*p2pstreamv1.AgentUpdateRootActionReceipt, error) {
	payload, err := agentupdateauth.RootActionReceiptPayload(record.Receipt)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return nil, errors.New("root action receipt canonical payload mismatch")
	}
	action, err := authorizationActionToProto(record.Receipt.Action)
	if err != nil {
		return nil, err
	}
	resultKind := p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_UNSPECIFIED
	switch record.Receipt.ResultKind {
	case agentupdateauth.RootActionResultSignedRelease:
		resultKind = p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_SIGNED_RELEASE
	case agentupdateauth.RootActionResultBootstrap:
		resultKind = p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_BOOTSTRAP
	default:
		return nil, errors.New("unsupported root action receipt result kind")
	}
	r := record.Receipt
	return &p2pstreamv1.AgentUpdateRootActionReceipt{
		AgentPublicId: r.AgentPublicID, AssignmentId: r.AssignmentID, CampaignId: r.CampaignID,
		Generation: r.Generation, Action: action, CommandSequence: r.CommandSequence,
		AuthorizationSha256: r.AuthorizationSHA256, AuthorizationNonce: append([]byte(nil), r.AuthorizationNonce...),
		AuthorityKeyId: r.AuthorityKeyID, AuthorityEpoch: r.AuthorityEpoch, ActivatorKeyId: r.ActivatorKeyID,
		RootActionCounter: r.RootActionCounter, CompletedAtUnixMillis: r.CompletedAtUnixMillis,
		ResultKind: resultKind, ResultRootVersion: r.ResultRootVersion, ResultManifestSha256: r.ResultManifestSHA256,
		ResultVersion: r.ResultVersion, ResultCommit: r.ResultCommit, ResultReleaseSequence: r.ResultReleaseSequence,
		ResultSecurityEpoch: r.ResultSecurityEpoch, ResultOs: r.ResultOS, ResultArch: r.ResultArch,
		ResultArtifactName: r.ResultArtifactName, ResultArtifactSize: r.ResultArtifactSize,
		ResultArtifactSha256: r.ResultArtifactSHA256, CanonicalPayload: append([]byte(nil), record.CanonicalPayload...),
		Signature: append([]byte(nil), record.Signature...),
	}, nil
}

func authorizationActionFromProto(action p2pstreamv1.AgentUpdateDesiredAction) (agentupdateauth.AssignmentAction, error) {
	switch action {
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE:
		return agentupdateauth.AssignmentActionActivate, nil
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK:
		return agentupdateauth.AssignmentActionRollback, nil
	default:
		return "", errors.New("management authorization contains an unsupported root action")
	}
}

func authorizationActionToProto(action agentupdateauth.AssignmentAction) (p2pstreamv1.AgentUpdateDesiredAction, error) {
	switch action {
	case agentupdateauth.AssignmentActionActivate:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE, nil
	case agentupdateauth.AssignmentActionRollback:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK, nil
	default:
		return p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_UNSPECIFIED, errors.New("root action receipt contains an unsupported action")
	}
}
