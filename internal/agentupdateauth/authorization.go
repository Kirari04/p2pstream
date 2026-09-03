package agentupdateauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/mod/semver"
)

// AssignmentAction is the privileged root action authorized by management.
type AssignmentAction string

// RootActionResultKind distinguishes a verified release slot from the initial
// installer-created bootstrap slot, which has no release root or manifest.
type RootActionResultKind string

const (
	AssignmentActionActivate AssignmentAction = "activate"
	AssignmentActionRollback AssignmentAction = "rollback"

	RootActionResultSignedRelease RootActionResultKind = "signed_release"
	RootActionResultBootstrap     RootActionResultKind = "bootstrap"

	// MaxAuthorizationLifetime bounds replay exposure if an authorization is
	// copied before the root activator consumes its monotonic command sequence.
	MaxAuthorizationLifetime = 24 * time.Hour
	// MaxEnrollmentReceiptLifetime matches the bounded enrollment recovery
	// window. A receipt outside this window must be replaced by an administrator.
	MaxEnrollmentReceiptLifetime = 24 * time.Hour
	// MaxSigningClockSkew permits small management/host clock differences without
	// accepting arbitrarily future-dated signed control records.
	MaxSigningClockSkew = 5 * time.Minute
)

var (
	hexDigestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexCommitPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	agentPublicIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	platformPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	artifactNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	pinnedRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
)

// AssignmentAuthorization is the complete management decision that may cross
// the unprivileged-worker/root-activator boundary. The signed target is exact:
// callers must not replace any target, compatibility, or artifact field.
type AssignmentAuthorization struct {
	AgentPublicID       string
	AssignmentID        int64
	CampaignID          int64
	Generation          int64
	Action              AssignmentAction
	CommandSequence     uint64
	Nonce               []byte
	IssuedAtUnixMillis  int64
	ExpiresAtUnixMillis int64
	AuthorityKeyID      string
	AuthorityEpoch      uint64
	ServerVersion       string
	RootVersion         uint64
	ManifestSHA256      string
	TargetVersion       string
	TargetCommit        string
	ReleaseSequence     uint64
	SecurityEpoch       uint64
	OS                  string
	Arch                string
	ArtifactName        string
	ArtifactSize        int64
	ArtifactSHA256      string
}

// AssignmentAuthorizationVerifyPolicy supplies local anti-replay and identity
// expectations. LastCommandSequence is the last sequence durably consumed by
// the verifier; an authorization must be strictly newer.
type AssignmentAuthorizationVerifyPolicy struct {
	Now                    time.Time
	ExpectedAgentPublicID  string
	ExpectedAction         AssignmentAction
	ExpectedAuthorityEpoch uint64
	LastCommandSequence    uint64
}

// EnrollmentReceipt is management's signed binding between one enrollment
// generation and the exact updater, root activator, release root, repository,
// platform, and management authority identities installed on the host.
type EnrollmentReceipt struct {
	AgentPublicID            string
	UpdaterKeyID             string
	UpdaterPublicKeySHA256   string
	ActivatorKeyID           string
	ActivatorPublicKeySHA256 string
	OS                       string
	Arch                     string
	UpdaterVersion           string
	TrustedRootSHA256        string
	TrustedRootVersion       uint64
	PinnedRepository         string
	AuthorityKeyID           string
	AuthorityEpoch           uint64
	EnrolledAtUnixMillis     int64
	ExpiresAtUnixMillis      int64
	Generation               uint64
}

// EnrollmentReceiptVerifyPolicy binds a receipt to local bootstrap state and
// rejects enrollment-generation replay.
type EnrollmentReceiptVerifyPolicy struct {
	Now                    time.Time
	ExpectedAgentPublicID  string
	ExpectedAuthorityEpoch uint64
	LastGeneration         uint64
}

// RootActionReceipt is the root activator's durable proof that it consumed an
// exact management authorization and reached the stated signed-release result.
// AuthorizationSHA256 transitively binds the complete assignment target.
type RootActionReceipt struct {
	AgentPublicID         string
	AssignmentID          int64
	CampaignID            int64
	Generation            int64
	Action                AssignmentAction
	CommandSequence       uint64
	AuthorizationSHA256   string
	AuthorizationNonce    []byte
	AuthorityKeyID        string
	AuthorityEpoch        uint64
	ActivatorKeyID        string
	RootActionCounter     uint64
	CompletedAtUnixMillis int64
	ResultKind            RootActionResultKind
	ResultRootVersion     uint64
	ResultManifestSHA256  string
	ResultVersion         string
	ResultCommit          string
	ResultReleaseSequence uint64
	ResultSecurityEpoch   uint64
	ResultOS              string
	ResultArch            string
	ResultArtifactName    string
	ResultArtifactSize    int64
	ResultArtifactSHA256  string
}

// RootActionReceiptVerifyPolicy binds the receipt to the authorization already
// authenticated by management and rejects root-action counter replay.
type RootActionReceiptVerifyPolicy struct {
	Now                         time.Time
	ExpectedAgentPublicID       string
	ExpectedAction              AssignmentAction
	ExpectedAuthorizationSHA256 string
	ExpectedAuthorityKeyID      string
	ExpectedAuthorityEpoch      uint64
	LastRootActionCounter       uint64
}

// KeyID returns the canonical lowercase SHA-256 identifier for an Ed25519 key.
func KeyID(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("Ed25519 public key must be exactly 32 bytes")
	}
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:]), nil
}

func AssignmentAuthorizationPayload(value AssignmentAuthorization) ([]byte, error) {
	if err := validateAssignmentAuthorization(value); err != nil {
		return nil, err
	}
	return record("management-assignment-authorization",
		value.AgentPublicID,
		strconv.FormatInt(value.AssignmentID, 10),
		strconv.FormatInt(value.CampaignID, 10),
		strconv.FormatInt(value.Generation, 10),
		string(value.Action),
		strconv.FormatUint(value.CommandSequence, 10),
		hex.EncodeToString(value.Nonce),
		strconv.FormatInt(value.IssuedAtUnixMillis, 10),
		strconv.FormatInt(value.ExpiresAtUnixMillis, 10),
		value.AuthorityKeyID,
		strconv.FormatUint(value.AuthorityEpoch, 10),
		value.ServerVersion,
		strconv.FormatUint(value.RootVersion, 10),
		value.ManifestSHA256,
		value.TargetVersion,
		value.TargetCommit,
		strconv.FormatUint(value.ReleaseSequence, 10),
		strconv.FormatUint(value.SecurityEpoch, 10),
		value.OS,
		value.Arch,
		value.ArtifactName,
		strconv.FormatInt(value.ArtifactSize, 10),
		value.ArtifactSHA256,
	), nil
}

func AssignmentAuthorizationDigest(value AssignmentAuthorization) ([sha256.Size]byte, error) {
	payload, err := AssignmentAuthorizationPayload(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func SignAssignmentAuthorization(privateKey ed25519.PrivateKey, value AssignmentAuthorization) ([]byte, error) {
	payload, err := AssignmentAuthorizationPayload(value)
	if err != nil {
		return nil, err
	}
	if err := privateKeyMatchesID(privateKey, value.AuthorityKeyID); err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, payload), nil
}

func VerifyAssignmentAuthorization(publicKey ed25519.PublicKey, value AssignmentAuthorization, signature []byte, policy AssignmentAuthorizationVerifyPolicy) error {
	payload, err := AssignmentAuthorizationPayload(value)
	if err != nil {
		return err
	}
	if err := verifyKeyAndSignature(publicKey, value.AuthorityKeyID, payload, signature); err != nil {
		return err
	}
	if policy.Now.IsZero() {
		return errors.New("assignment authorization verification time is required")
	}
	if !agentPublicIDPattern.MatchString(policy.ExpectedAgentPublicID) || !validAction(policy.ExpectedAction) || policy.ExpectedAuthorityEpoch == 0 {
		return errors.New("assignment authorization verification policy is incomplete")
	}
	if value.IssuedAtUnixMillis > policy.Now.Add(MaxSigningClockSkew).UnixMilli() || value.ExpiresAtUnixMillis <= policy.Now.UnixMilli() {
		return errors.New("assignment authorization is not currently valid")
	}
	if value.AgentPublicID != policy.ExpectedAgentPublicID {
		return errors.New("assignment authorization is for a different agent")
	}
	if value.Action != policy.ExpectedAction {
		return errors.New("assignment authorization permits a different action")
	}
	if value.AuthorityEpoch != policy.ExpectedAuthorityEpoch {
		return errors.New("assignment authorization authority epoch does not match pinned state")
	}
	if value.CommandSequence <= policy.LastCommandSequence {
		return errors.New("assignment authorization command sequence was replayed")
	}
	return nil
}

func EnrollmentReceiptPayload(value EnrollmentReceipt) ([]byte, error) {
	if err := validateEnrollmentReceipt(value); err != nil {
		return nil, err
	}
	return record("management-enrollment-receipt",
		value.AgentPublicID,
		value.UpdaterKeyID,
		value.UpdaterPublicKeySHA256,
		value.ActivatorKeyID,
		value.ActivatorPublicKeySHA256,
		value.OS,
		value.Arch,
		value.UpdaterVersion,
		value.TrustedRootSHA256,
		strconv.FormatUint(value.TrustedRootVersion, 10),
		value.PinnedRepository,
		value.AuthorityKeyID,
		strconv.FormatUint(value.AuthorityEpoch, 10),
		strconv.FormatInt(value.EnrolledAtUnixMillis, 10),
		strconv.FormatInt(value.ExpiresAtUnixMillis, 10),
		strconv.FormatUint(value.Generation, 10),
	), nil
}

func SignEnrollmentReceipt(privateKey ed25519.PrivateKey, value EnrollmentReceipt) ([]byte, error) {
	payload, err := EnrollmentReceiptPayload(value)
	if err != nil {
		return nil, err
	}
	if err := privateKeyMatchesID(privateKey, value.AuthorityKeyID); err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, payload), nil
}

func VerifyEnrollmentReceipt(publicKey ed25519.PublicKey, value EnrollmentReceipt, signature []byte, policy EnrollmentReceiptVerifyPolicy) error {
	payload, err := EnrollmentReceiptPayload(value)
	if err != nil {
		return err
	}
	if err := verifyKeyAndSignature(publicKey, value.AuthorityKeyID, payload, signature); err != nil {
		return err
	}
	if policy.Now.IsZero() {
		return errors.New("enrollment receipt verification time is required")
	}
	if !agentPublicIDPattern.MatchString(policy.ExpectedAgentPublicID) || policy.ExpectedAuthorityEpoch == 0 {
		return errors.New("enrollment receipt verification policy is incomplete")
	}
	if value.EnrolledAtUnixMillis > policy.Now.Add(MaxSigningClockSkew).UnixMilli() || value.ExpiresAtUnixMillis <= policy.Now.UnixMilli() {
		return errors.New("enrollment receipt is not currently valid")
	}
	if value.AgentPublicID != policy.ExpectedAgentPublicID {
		return errors.New("enrollment receipt is for a different agent")
	}
	if value.AuthorityEpoch != policy.ExpectedAuthorityEpoch {
		return errors.New("enrollment receipt authority epoch does not match pinned state")
	}
	if value.Generation <= policy.LastGeneration {
		return errors.New("enrollment receipt generation was replayed")
	}
	return nil
}

func RootActionReceiptPayload(value RootActionReceipt) ([]byte, error) {
	if err := validateRootActionReceipt(value); err != nil {
		return nil, err
	}
	return record("root-action-receipt",
		value.AgentPublicID,
		strconv.FormatInt(value.AssignmentID, 10),
		strconv.FormatInt(value.CampaignID, 10),
		strconv.FormatInt(value.Generation, 10),
		string(value.Action),
		strconv.FormatUint(value.CommandSequence, 10),
		value.AuthorizationSHA256,
		hex.EncodeToString(value.AuthorizationNonce),
		value.AuthorityKeyID,
		strconv.FormatUint(value.AuthorityEpoch, 10),
		value.ActivatorKeyID,
		strconv.FormatUint(value.RootActionCounter, 10),
		strconv.FormatInt(value.CompletedAtUnixMillis, 10),
		string(value.ResultKind),
		strconv.FormatUint(value.ResultRootVersion, 10),
		value.ResultManifestSHA256,
		value.ResultVersion,
		value.ResultCommit,
		strconv.FormatUint(value.ResultReleaseSequence, 10),
		strconv.FormatUint(value.ResultSecurityEpoch, 10),
		value.ResultOS,
		value.ResultArch,
		value.ResultArtifactName,
		strconv.FormatInt(value.ResultArtifactSize, 10),
		value.ResultArtifactSHA256,
	), nil
}

func SignRootActionReceipt(privateKey ed25519.PrivateKey, value RootActionReceipt) ([]byte, error) {
	payload, err := RootActionReceiptPayload(value)
	if err != nil {
		return nil, err
	}
	if err := privateKeyMatchesID(privateKey, value.ActivatorKeyID); err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, payload), nil
}

func VerifyRootActionReceipt(publicKey ed25519.PublicKey, value RootActionReceipt, signature []byte, policy RootActionReceiptVerifyPolicy) error {
	payload, err := RootActionReceiptPayload(value)
	if err != nil {
		return err
	}
	if err := verifyKeyAndSignature(publicKey, value.ActivatorKeyID, payload, signature); err != nil {
		return err
	}
	if policy.Now.IsZero() {
		return errors.New("root action receipt verification time is required")
	}
	if !agentPublicIDPattern.MatchString(policy.ExpectedAgentPublicID) || !validAction(policy.ExpectedAction) ||
		!hexDigestPattern.MatchString(policy.ExpectedAuthorizationSHA256) || !hexDigestPattern.MatchString(policy.ExpectedAuthorityKeyID) ||
		policy.ExpectedAuthorityEpoch == 0 {
		return errors.New("root action receipt verification policy is incomplete")
	}
	if value.CompletedAtUnixMillis > policy.Now.Add(MaxSigningClockSkew).UnixMilli() {
		return errors.New("root action receipt completion time is in the future")
	}
	if value.AgentPublicID != policy.ExpectedAgentPublicID {
		return errors.New("root action receipt is for a different agent")
	}
	if value.Action != policy.ExpectedAction {
		return errors.New("root action receipt records a different action")
	}
	if value.AuthorizationSHA256 != policy.ExpectedAuthorizationSHA256 {
		return errors.New("root action receipt does not bind the expected authorization")
	}
	if value.AuthorityKeyID != policy.ExpectedAuthorityKeyID {
		return errors.New("root action receipt authority key does not match")
	}
	if value.AuthorityEpoch != policy.ExpectedAuthorityEpoch {
		return errors.New("root action receipt authority epoch does not match")
	}
	if value.RootActionCounter <= policy.LastRootActionCounter {
		return errors.New("root action receipt counter was replayed")
	}
	return nil
}

func validateAssignmentAuthorization(value AssignmentAuthorization) error {
	if !agentPublicIDPattern.MatchString(value.AgentPublicID) || value.AssignmentID <= 0 || value.CampaignID <= 0 || value.Generation <= 0 {
		return errors.New("assignment authorization identity is invalid")
	}
	if !validAction(value.Action) || value.CommandSequence == 0 || value.CommandSequence > math.MaxInt64 || len(value.Nonce) != 32 {
		return errors.New("assignment authorization action, sequence, or nonce is invalid")
	}
	if err := validateInterval(value.IssuedAtUnixMillis, value.ExpiresAtUnixMillis, MaxAuthorizationLifetime); err != nil {
		return fmt.Errorf("assignment authorization lifetime: %w", err)
	}
	if !hexDigestPattern.MatchString(value.AuthorityKeyID) || value.AuthorityEpoch == 0 || value.AuthorityEpoch > math.MaxInt64 {
		return errors.New("assignment authorization authority is invalid")
	}
	if !validVersion(value.ServerVersion) || value.RootVersion == 0 || value.RootVersion > math.MaxInt64 ||
		!hexDigestPattern.MatchString(value.ManifestSHA256) || !validVersion(value.TargetVersion) || !hexCommitPattern.MatchString(value.TargetCommit) ||
		value.ReleaseSequence == 0 || value.ReleaseSequence > math.MaxInt64 || value.SecurityEpoch == 0 || value.SecurityEpoch > math.MaxInt64 {
		return errors.New("assignment authorization release target is invalid")
	}
	return validateArtifact(value.OS, value.Arch, value.ArtifactName, value.ArtifactSize, value.ArtifactSHA256)
}

func validateEnrollmentReceipt(value EnrollmentReceipt) error {
	if !agentPublicIDPattern.MatchString(value.AgentPublicID) || !hexDigestPattern.MatchString(value.UpdaterKeyID) ||
		!hexDigestPattern.MatchString(value.UpdaterPublicKeySHA256) || !hexDigestPattern.MatchString(value.ActivatorKeyID) ||
		!hexDigestPattern.MatchString(value.ActivatorPublicKeySHA256) || value.UpdaterKeyID != value.UpdaterPublicKeySHA256 ||
		value.ActivatorKeyID != value.ActivatorPublicKeySHA256 || value.UpdaterKeyID == value.ActivatorKeyID {
		return errors.New("enrollment receipt identity keys are invalid")
	}
	if !platformPattern.MatchString(value.OS) || !platformPattern.MatchString(value.Arch) || !validVersion(value.UpdaterVersion) {
		return errors.New("enrollment receipt platform or updater version is invalid")
	}
	if !hexDigestPattern.MatchString(value.TrustedRootSHA256) || value.TrustedRootVersion == 0 || value.TrustedRootVersion > math.MaxInt64 ||
		!pinnedRepositoryPattern.MatchString(value.PinnedRepository) {
		return errors.New("enrollment receipt release trust is invalid")
	}
	if !hexDigestPattern.MatchString(value.AuthorityKeyID) || value.AuthorityEpoch == 0 || value.AuthorityEpoch > math.MaxInt64 ||
		value.Generation == 0 || value.Generation > math.MaxInt64 {
		return errors.New("enrollment receipt authority or generation is invalid")
	}
	if err := validateInterval(value.EnrolledAtUnixMillis, value.ExpiresAtUnixMillis, MaxEnrollmentReceiptLifetime); err != nil {
		return fmt.Errorf("enrollment receipt lifetime: %w", err)
	}
	return nil
}

func validateRootActionReceipt(value RootActionReceipt) error {
	if !agentPublicIDPattern.MatchString(value.AgentPublicID) || value.AssignmentID <= 0 || value.CampaignID <= 0 || value.Generation <= 0 ||
		!validAction(value.Action) || value.CommandSequence == 0 || value.CommandSequence > math.MaxInt64 || len(value.AuthorizationNonce) != 32 {
		return errors.New("root action receipt assignment binding is invalid")
	}
	if !hexDigestPattern.MatchString(value.AuthorizationSHA256) || !hexDigestPattern.MatchString(value.AuthorityKeyID) ||
		value.AuthorityEpoch == 0 || value.AuthorityEpoch > math.MaxInt64 || !hexDigestPattern.MatchString(value.ActivatorKeyID) ||
		value.RootActionCounter == 0 || value.RootActionCounter > math.MaxInt64 {
		return errors.New("root action receipt authority or counter is invalid")
	}
	if !validUnixMillis(value.CompletedAtUnixMillis) {
		return errors.New("root action receipt result is invalid")
	}
	switch value.ResultKind {
	case RootActionResultSignedRelease:
		if value.ResultRootVersion == 0 || value.ResultRootVersion > math.MaxInt64 || !hexDigestPattern.MatchString(value.ResultManifestSHA256) ||
			!validVersion(value.ResultVersion) || !hexCommitPattern.MatchString(value.ResultCommit) || value.ResultReleaseSequence == 0 ||
			value.ResultReleaseSequence > math.MaxInt64 || value.ResultSecurityEpoch == 0 || value.ResultSecurityEpoch > math.MaxInt64 {
			return errors.New("root action receipt signed-release result is invalid")
		}
	case RootActionResultBootstrap:
		if value.Action != AssignmentActionRollback || value.ResultRootVersion != 0 || value.ResultManifestSHA256 != "" ||
			value.ResultReleaseSequence != 0 || value.ResultSecurityEpoch != 0 || !validVersion(value.ResultVersion) ||
			!hexCommitPattern.MatchString(value.ResultCommit) {
			return errors.New("root action receipt bootstrap result is invalid")
		}
	default:
		return errors.New("root action receipt result kind is invalid")
	}
	return validateArtifact(value.ResultOS, value.ResultArch, value.ResultArtifactName, value.ResultArtifactSize, value.ResultArtifactSHA256)
}

func validateArtifact(osName, arch, name string, size int64, digest string) error {
	if !platformPattern.MatchString(osName) || !platformPattern.MatchString(arch) || !artifactNamePattern.MatchString(name) ||
		size <= 0 || size > 512<<20 || !hexDigestPattern.MatchString(digest) {
		return errors.New("signed artifact is invalid")
	}
	return nil
}

func validateInterval(start, end int64, maximum time.Duration) error {
	if !validUnixMillis(start) || !validUnixMillis(end) || end <= start {
		return errors.New("timestamps are invalid")
	}
	if end-start > maximum.Milliseconds() {
		return errors.New("interval exceeds the maximum lifetime")
	}
	return nil
}

func validUnixMillis(value int64) bool {
	// 9999-12-31T23:59:59.999Z is the largest canonical application time.
	return value > 0 && value <= 253402300799999
}

func validAction(action AssignmentAction) bool {
	return action == AssignmentActionActivate || action == AssignmentActionRollback
}

func validVersion(version string) bool {
	return len(version) <= 128 && semver.IsValid(version)
}

func privateKeyMatchesID(privateKey ed25519.PrivateKey, expectedID string) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("Ed25519 private key must be exactly 64 bytes")
	}
	canonical := ed25519.NewKeyFromSeed(privateKey.Seed())
	if subtle.ConstantTimeCompare(canonical, privateKey) != 1 {
		return errors.New("Ed25519 private key is not canonical")
	}
	keyID, _ := KeyID(canonical.Public().(ed25519.PublicKey))
	if keyID != expectedID {
		return errors.New("signing key does not match the embedded key ID")
	}
	return nil
}

func verifyKeyAndSignature(publicKey ed25519.PublicKey, expectedID string, payload, signature []byte) error {
	keyID, err := KeyID(publicKey)
	if err != nil {
		return err
	}
	if keyID != expectedID {
		return errors.New("verification key does not match the embedded key ID")
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("invalid Ed25519 signature")
	}
	return nil
}
