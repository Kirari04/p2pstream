package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
)

type ControlAPI interface {
	EnrollAgentUpdater(context.Context, *connect.Request[p2pstreamv1.EnrollAgentUpdaterRequest]) (*connect.Response[p2pstreamv1.EnrollAgentUpdaterResponse], error)
	CheckAgentUpdate(context.Context, *connect.Request[p2pstreamv1.CheckAgentUpdateRequest]) (*connect.Response[p2pstreamv1.CheckAgentUpdateResponse], error)
	ReportAgentUpdate(context.Context, *connect.Request[p2pstreamv1.ReportAgentUpdateRequest]) (*connect.Response[p2pstreamv1.ReportAgentUpdateResponse], error)
}

type workerCounter struct {
	Counter uint64 `json:"counter"`
}

func (p Paths) workerCounterPath() string {
	return filepath.Join(p.workerStateDir(), "control-counter.json")
}
func (p Paths) enrollmentReceiptPath() string {
	return filepath.Join(p.workerStateDir(), "enrollment.json")
}
func (p Paths) enrolledPath() string {
	return filepath.Join(filepath.Dir(p.ConfigPath), "enrolled.json")
}
func (p Paths) enrollmentTokenPath() string {
	return filepath.Join(filepath.Dir(p.ConfigPath), "enrollment.token")
}

func NewControlAPI(config HostConfig) (ControlAPI, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	// The agent installer already pins a private management CA at this exact
	// root-owned path when one is required. System roots remain available.
	if pem, err := readRegularNoFollow("/etc/p2pstream/management-ca.pem", 1<<20); err == nil {
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("management CA file contains no certificate")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read management CA: %w", err)
	}
	transport := newControlTransport(pool)
	client := newControlHTTPClient(transport)
	return p2pstreamv1connect.NewAgentManagementServiceClient(client, config.ManagementOrigin), nil
}

func newControlTransport(pool *x509.CertPool) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Managed update trust must not be silently delegated to process-wide proxy
	// environment variables. A future explicit proxy setting needs its own
	// authenticated, pinned configuration surface.
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return transport
}

func newControlHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		// Enrollment tokens and signed control requests are scoped to one pinned
		// management origin. Never replay them after any HTTP redirect.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type WorkerControl struct {
	Paths          Paths
	API            ControlAPI
	UpdaterVersion string
}

func (c WorkerControl) privateKey() (ed25519.PrivateKey, error) {
	info, err := os.Lstat(c.Paths.workerPrivateKeyPath())
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("worker updater private key has unsafe ownership or permissions")
	}
	return loadActivatorPrivateKey(c.Paths.workerPrivateKeyPath())
}

func (c WorkerControl) reserveCounter() (uint64, error) {
	data, err := readRegularNoFollow(c.Paths.workerCounterPath(), 64<<10)
	var state workerCounter
	if err == nil {
		if err := strictJSON(data, &state); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if state.Counter == ^uint64(0) {
		return 0, errors.New("updater control counter is exhausted")
	}
	state.Counter++
	if err := atomicJSON(c.Paths.workerCounterPath(), state, 0600); err != nil {
		return 0, err
	}
	return state.Counter, nil
}

func (c WorkerControl) Enroll(ctx context.Context, config HostConfig) error {
	if c.API == nil {
		return errors.New("updater control API is required")
	}
	private, err := c.privateKey()
	if err != nil {
		return err
	}
	activatorPrivate, err := loadActivatorPrivateKey(c.Paths.activatorPrivateKeyPath())
	if err != nil {
		// The unprivileged worker must never be able to read this key. Enrollment
		// uses the public copy emitted by bootstrap instead.
		publicText, readErr := readRegularNoFollow(c.Paths.activatorPublicKeyPath(), 128)
		if readErr != nil {
			return fmt.Errorf("read activator public key: %w", readErr)
		}
		activatorPublic, parseErr := parsePublicKeyText(publicText)
		if parseErr != nil {
			return parseErr
		}
		return c.enrollWithKeys(ctx, config, private, activatorPublic)
	}
	return c.enrollWithKeys(ctx, config, private, activatorPrivate.Public().(ed25519.PublicKey))
}

func (c WorkerControl) enrollWithKeys(ctx context.Context, config HostConfig, private ed25519.PrivateKey, activatorPublic ed25519.PublicKey) error {
	token, tokenErr := readRegularNoFollow(c.Paths.enrollmentTokenPath(), 4096)
	switch {
	case tokenErr == nil:
		tokenText := strings.TrimSuffix(string(token), "\n")
		if tokenText == "" || strings.ContainsAny(tokenText, "\r\n") {
			return errors.New("invalid single-use updater enrollment token")
		}
		tokenDigest := sha256.Sum256([]byte(tokenText))
		tokenDigestText := hex.EncodeToString(tokenDigest[:])
		if existingData, readErr := readRegularNoFollow(c.Paths.enrollmentReceiptPath(), 64<<10); readErr == nil {
			var existing enrollmentReceiptRecord
			if strictJSON(existingData, &existing) != nil {
				return errors.New("existing updater enrollment receipt is invalid")
			}
			if existing.EnrollmentTokenSHA256 == tokenDigestText {
				if err := validateEnrollmentReceiptRecord(c.Paths, existing, config, private.Public().(ed25519.PublicKey), activatorPublic, c.UpdaterVersion, time.Now().UTC(), 0); err != nil {
					return err
				}
				break
			}
		}
		lastGeneration, err := enrollmentGenerationFloor(c.Paths, config, c.Paths.enrollmentReceiptPath(), c.Paths.enrolledPath())
		if err != nil {
			return err
		}
		response, err := c.API.EnrollAgentUpdater(ctx, connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{
			Token: tokenText, AgentPublicId: config.AgentPublicID,
			UpdaterPublicKey: private.Public().(ed25519.PublicKey), ActivatorPublicKey: activatorPublic,
			Os: runtime.GOOS, Arch: runtime.GOARCH, UpdaterVersion: c.UpdaterVersion,
		}))
		if err != nil {
			return fmt.Errorf("enroll updater: %w", err)
		}
		receipt, err := enrollmentReceiptFromProto(response.Msg.Receipt)
		if err != nil {
			return err
		}
		if response.Msg.UpdaterKeyId != receipt.Receipt.UpdaterKeyID || response.Msg.ActivatorKeyId != receipt.Receipt.ActivatorKeyID ||
			response.Msg.EnrolledAtUnixMillis != receipt.Receipt.EnrolledAtUnixMillis {
			return errors.New("unsigned enrollment response fields do not match its signed receipt")
		}
		if err := validateEnrollmentReceiptRecord(c.Paths, receipt, config, private.Public().(ed25519.PublicKey), activatorPublic, c.UpdaterVersion, time.Now().UTC(), lastGeneration); err != nil {
			return err
		}
		receipt.EnrollmentTokenSHA256 = tokenDigestText
		if err := atomicJSON(c.Paths.enrollmentReceiptPath(), receipt, 0600); err != nil {
			return err
		}
	case !errors.Is(tokenErr, os.ErrNotExist):
		return tokenErr
	default:
		if _, err := readRegularNoFollow(c.Paths.enrollmentReceiptPath(), 64<<10); err != nil {
			return fmt.Errorf("updater is neither enrolled nor carrying a re-enrollment token: %w", err)
		}
	}
	if _, err := c.Check(ctx, config.AgentPublicID); err != nil {
		return fmt.Errorf("first signed updater check: %w", err)
	}
	data, err := readRegularNoFollow(c.Paths.enrollmentReceiptPath(), 64<<10)
	if err != nil {
		return err
	}
	var receipt enrollmentReceiptRecord
	if err := strictJSON(data, &receipt); err != nil {
		return err
	}
	receipt.FirstCheckAt = time.Now().UTC().Format(time.RFC3339Nano)
	return atomicJSON(c.Paths.enrollmentReceiptPath(), receipt, 0600)
}

// enrollmentGenerationFloor authenticates prior receipts without requiring
// them to remain unexpired or to describe the new updater keys/version. Their
// sole purpose here is a monotonic generation floor under the same pinned
// management authority; an unprivileged worker cannot forge a larger floor.
func enrollmentGenerationFloor(paths Paths, config HostConfig, candidates ...string) (uint64, error) {
	pinned, authorityPublic, err := loadManagementAuthority(paths)
	if err != nil {
		return 0, fmt.Errorf("load pinned updater management authority: %w", err)
	}
	var floor uint64
	for _, candidate := range candidates {
		data, readErr := readRegularNoFollow(candidate, 64<<10)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return 0, readErr
		}
		var record enrollmentReceiptRecord
		if err := strictJSON(data, &record); err != nil {
			return 0, fmt.Errorf("decode prior updater enrollment receipt: %w", err)
		}
		payload, err := agentupdateauth.EnrollmentReceiptPayload(record.Receipt)
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(payload, record.CanonicalPayload) || !ed25519.Verify(authorityPublic, payload, record.Signature) {
			return 0, errors.New("prior updater enrollment receipt is not authentically signed")
		}
		r := record.Receipt
		if r.AgentPublicID != config.AgentPublicID || r.AuthorityKeyID != pinned.KeyID || r.AuthorityEpoch != pinned.Epoch ||
			r.PinnedRepository != config.Repository {
			return 0, errors.New("prior updater enrollment receipt does not match pinned host trust")
		}
		if r.Generation > floor {
			floor = r.Generation
		}
	}
	return floor, nil
}

func (c WorkerControl) Check(ctx context.Context, agentPublicID string) (*p2pstreamv1.CheckAgentUpdateResponse, error) {
	private, err := c.privateKey()
	if err != nil {
		return nil, err
	}
	counter, err := c.reserveCounter()
	if err != nil {
		return nil, err
	}
	request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agentPublicID, Counter: counter}
	request.Signature = ed25519.Sign(private, agentupdateauth.CheckPayload(agentPublicID, counter))
	response, err := c.API.CheckAgentUpdate(ctx, connect.NewRequest(request))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

func (c WorkerControl) Report(ctx context.Context, report agentupdateauth.Report) (*p2pstreamv1.ReportAgentUpdateResponse, error) {
	return c.report(ctx, report, nil)
}

func (c WorkerControl) ReportRootAction(ctx context.Context, report agentupdateauth.Report, receipt rootActionReceiptRecord) (*p2pstreamv1.ReportAgentUpdateResponse, error) {
	message, err := rootActionReceiptToProto(receipt)
	if err != nil {
		return nil, err
	}
	return c.report(ctx, report, message)
}

func (c WorkerControl) report(ctx context.Context, report agentupdateauth.Report, rootReceipt *p2pstreamv1.AgentUpdateRootActionReceipt) (*p2pstreamv1.ReportAgentUpdateResponse, error) {
	private, err := c.privateKey()
	if err != nil {
		return nil, err
	}
	counter, err := c.reserveCounter()
	if err != nil {
		return nil, err
	}
	report.Counter = counter
	signature := ed25519.Sign(private, agentupdateauth.ReportPayload(report))
	request := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: report.AgentPublicID, Counter: counter, Signature: signature,
		AssignmentId: report.AssignmentID, Generation: report.Generation,
		State: p2pstreamv1.AgentUpdaterReportState(report.State), ManifestSha256: report.ManifestSHA256,
		BinarySha256: report.BinarySHA256, RunningVersion: report.RunningVersion, RunningCommit: report.RunningCommit,
		FailureCode: report.FailureCode, FailureDetail: report.FailureDetail,
		ActivationCounter: report.ActivationCounter, ActivationNonce: report.ActivationNonce,
		ActivatorSignature: report.ActivatorSignature, RootActionReceipt: rootReceipt,
	}
	response, err := c.API.ReportAgentUpdate(ctx, connect.NewRequest(request))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

func parsePublicKeyText(data []byte) (ed25519.PublicKey, error) {
	return agentupdate.ParsePublicKey(strings.TrimSuffix(string(data), "\n"))
}

func keyID(public ed25519.PublicKey) string {
	digest := sha256.Sum256(public)
	return hex.EncodeToString(digest[:])
}

func validateEnrollmentReceiptRecord(paths Paths, record enrollmentReceiptRecord, config HostConfig, updaterPublic, activatorPublic ed25519.PublicKey, updaterVersion string, now time.Time, lastGeneration uint64) error {
	payload, err := agentupdateauth.EnrollmentReceiptPayload(record.Receipt)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return errors.New("signed updater enrollment receipt canonical payload mismatch")
	}
	pinned, authorityPublic, err := loadManagementAuthority(paths)
	if err != nil {
		return fmt.Errorf("load pinned updater management authority: %w", err)
	}
	if err := agentupdateauth.VerifyEnrollmentReceipt(authorityPublic, record.Receipt, record.Signature, agentupdateauth.EnrollmentReceiptVerifyPolicy{
		Now: now, ExpectedAgentPublicID: config.AgentPublicID, ExpectedAuthorityEpoch: pinned.Epoch, LastGeneration: lastGeneration,
	}); err != nil {
		return fmt.Errorf("verify signed updater enrollment receipt: %w", err)
	}
	updaterKeyID, _ := agentupdateauth.KeyID(updaterPublic)
	activatorKeyID, _ := agentupdateauth.KeyID(activatorPublic)
	r := record.Receipt
	if r.UpdaterKeyID != updaterKeyID || r.UpdaterPublicKeySHA256 != updaterKeyID ||
		r.ActivatorKeyID != activatorKeyID || r.ActivatorPublicKeySHA256 != activatorKeyID ||
		r.OS != runtime.GOOS || r.Arch != runtime.GOARCH || r.UpdaterVersion != updaterVersion ||
		r.PinnedRepository != config.Repository || r.AuthorityKeyID != pinned.KeyID || r.AuthorityEpoch != pinned.Epoch {
		return errors.New("signed updater enrollment receipt does not exactly match local bootstrap state")
	}
	return nil
}

func FinalizeEnrollment(paths Paths) error {
	if os.Geteuid() != 0 {
		return errors.New("updater enrollment finalization must run as root")
	}
	config, err := LoadHostConfig(paths.ConfigPath)
	if err != nil {
		return err
	}
	workerPrivate, err := loadActivatorPrivateKey(paths.workerPrivateKeyPath())
	if err != nil {
		return err
	}
	activatorPrivate, err := loadActivatorPrivateKey(paths.activatorPrivateKeyPath())
	if err != nil {
		return err
	}
	data, err := readRegularNoFollow(paths.enrollmentReceiptPath(), 64<<10)
	if err != nil {
		return err
	}
	var receipt enrollmentReceiptRecord
	if err := strictJSON(data, &receipt); err != nil {
		return err
	}
	if receipt.FirstCheckAt == "" {
		return errors.New("signed updater check has not succeeded")
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.FirstCheckAt); err != nil {
		return errors.New("invalid first signed check timestamp")
	}
	lastGeneration := uint64(0)
	if priorData, readErr := readRegularNoFollow(paths.enrolledPath(), 64<<10); readErr == nil {
		var prior enrollmentReceiptRecord
		if err := strictJSON(priorData, &prior); err != nil {
			return err
		}
		if !bytes.Equal(prior.CanonicalPayload, receipt.CanonicalPayload) || !bytes.Equal(prior.Signature, receipt.Signature) {
			lastGeneration, err = enrollmentGenerationFloor(paths, config, paths.enrolledPath())
			if err != nil {
				return err
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := validateEnrollmentReceiptRecord(paths, receipt, config, workerPrivate.Public().(ed25519.PublicKey), activatorPrivate.Public().(ed25519.PublicKey), buildinfo.Version, time.Now().UTC(), lastGeneration); err != nil {
		return err
	}
	if err := exec.Command("/usr/bin/systemctl", "enable", "p2pstream-updater.timer", "p2pstream-updater-activate.path").Run(); err != nil {
		return fmt.Errorf("enable updater units: %w", err)
	}
	configInfo, err := os.Stat(paths.ConfigPath)
	if err != nil {
		return err
	}
	configStat := configInfo.Sys().(*syscall.Stat_t)
	if err := atomicJSON(paths.enrolledPath(), receipt, 0640); err != nil {
		return err
	}
	if err := os.Chown(paths.enrolledPath(), 0, int(configStat.Gid)); err != nil {
		return err
	}
	if err := removeAndSync(paths.enrollmentTokenPath()); err != nil {
		return err
	}
	if err := exec.Command("/usr/bin/systemctl", "start", "p2pstream-updater.timer", "p2pstream-updater-activate.path").Run(); err != nil {
		return fmt.Errorf("start updater units: %w", err)
	}
	return nil
}

var _ = syscall.Stat_t{}
