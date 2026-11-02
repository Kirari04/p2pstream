package serverupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/google/uuid"
)

const EnrollmentPreflightAPI = 1
const EnrollmentPreflightMaxBytes = 16 << 10

// Each standalone installation owns its own observation socket. The enrolled
// executor's fixed RuntimeSocket remains a separate, unchanged protocol.
func EnrollmentPreflightSocket(installationID string) (string, error) {
	if !validEnrollmentInstallationID(installationID) {
		return "", errors.New("server enrollment preflight requires an existing installation identity")
	}
	return "/tmp/p2pstream-enrollment-" + installationID + ".sock", nil
}

// EnrollmentPreflight is an observation, never an admission or preparation token.
// It intentionally omits runtime agent/listener identities and arbitrary details.
type EnrollmentPreflight struct {
	API            int                 `json:"api"`
	InstallationID string              `json:"installation_id"`
	Version        string              `json:"version"`
	Commit         string              `json:"commit"`
	Ready          bool                `json:"ready"`
	Blockers       []EnrollmentBlocker `json:"blockers"`
}

type EnrollmentBlocker struct {
	Code    string `json:"code"`
	Action  string `json:"action"`
	Message string `json:"message"`
}

// Only these fixed operator messages may cross the local preflight boundary.
var enrollmentBlockers = []EnrollmentBlocker{
	{"database_schema_unavailable", "restore_database", "Database schema is unavailable"},
	{"agent_updates_active", "finish_agent_updates", "Finish or cancel active agent updates before updating the server; complete pending agent rollback recovery even if its campaign was cancelled"},
	{"certificate_rotation_active", "finish_certificate_rotation", "Finish management certificate rotation before updating the server"},
	{"public_listener_unavailable", "restore_public_listeners", "An enabled public listener is not running"},
	{"custom_database_path", "use_manual_deployment", "Automatic backups require the standard /data database; use a manual deployment for custom database paths"},
	{"custom_state_path", "use_manual_deployment", "Custom state paths outside /data require a manual deployment"},
	{"maintenance_active", "finish_server_recovery", "Finish server update maintenance or recovery before enrollment"},
	{"configuration_unavailable", "restore_configuration", "Server configuration is unavailable"},
	{"installation_identity_unavailable", "restore_installation_identity", "Server installation identity is unavailable"},
	{"runtime_unavailable", "restore_server_readiness", "Server readiness is unavailable; resolve server health before enrollment"},
}

func EnrollmentBlockerForCode(code string) EnrollmentBlocker {
	for _, blocker := range enrollmentBlockers {
		if blocker.Code == code {
			return blocker
		}
	}
	return enrollmentBlockers[len(enrollmentBlockers)-1]
}

// NewEnrollmentPreflight reuses runtime readiness but never echoes unknown text.
func NewEnrollmentPreflight(installationID string, status RuntimeStatus) EnrollmentPreflight {
	p := EnrollmentPreflight{API: EnrollmentPreflightAPI, InstallationID: installationID, Version: status.Version, Commit: status.Commit, Blockers: []EnrollmentBlocker{}}
	seen := map[string]bool{}
	add := func(code string) {
		blocker := EnrollmentBlockerForCode(code)
		if !seen[blocker.Code] {
			p.Blockers = append(p.Blockers, blocker)
			seen[blocker.Code] = true
		}
	}
	for _, message := range status.Blocked {
		code := "runtime_unavailable"
		for _, blocker := range enrollmentBlockers {
			if message == blocker.Message {
				code = blocker.Code
				break
			}
		}
		// Runtime /status retains its established message for older executors.
		if message == "Finish or cancel active agent updates before updating the server" {
			code = "agent_updates_active"
		}
		add(code)
	}
	if status.Maintenance {
		add("maintenance_active")
	}
	if !validEnrollmentInstallationID(installationID) {
		p.InstallationID = ""
		add("installation_identity_unavailable")
	}
	if !status.Ready {
		add("runtime_unavailable")
	}
	p.Ready = len(p.Blockers) == 0
	return p
}

func validEnrollmentInstallationID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id && u != uuid.Nil
}

var enrollmentVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,128}$`)

// DecodeEnrollmentPreflight rejects incomplete, duplicated, unknown, oversized,
// or inconsistent responses before the CLI prints any bytes from the socket.
func DecodeEnrollmentPreflight(r io.Reader) (EnrollmentPreflight, error) {
	var p EnrollmentPreflight
	invalid := errors.New("invalid server enrollment preflight response")
	data, err := io.ReadAll(io.LimitReader(r, EnrollmentPreflightMaxBytes+1))
	if err != nil || len(data) > EnrollmentPreflightMaxBytes {
		return p, invalid
	}
	if !exactPreflightObject(data, "api", "installation_id", "version", "commit", "ready", "blockers") || json.Unmarshal(data, &p) != nil {
		return EnrollmentPreflight{}, invalid
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	var rawBlockers []json.RawMessage
	if json.Unmarshal(fields["blockers"], &rawBlockers) != nil || p.Blockers == nil || len(p.Blockers) > len(enrollmentBlockers) {
		return EnrollmentPreflight{}, invalid
	}
	seen := map[string]bool{}
	for i, blocker := range p.Blockers {
		if !exactPreflightObject(rawBlockers[i], "code", "action", "message") || blocker != EnrollmentBlockerForCode(blocker.Code) || seen[blocker.Code] {
			return EnrollmentPreflight{}, invalid
		}
		seen[blocker.Code] = true
	}
	if p.API != EnrollmentPreflightAPI || !enrollmentVersionPattern.MatchString(p.Version) || (p.Commit != "" && !commitPattern.MatchString(p.Commit)) ||
		(p.InstallationID != "" && !validEnrollmentInstallationID(p.InstallationID)) ||
		(p.InstallationID == "" && !seen["installation_identity_unavailable"]) || p.Ready != (len(p.Blockers) == 0) {
		return EnrollmentPreflight{}, invalid
	}
	return p, nil
}

func exactPreflightObject(data []byte, names ...string) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	remaining := map[string]bool{}
	for _, name := range names {
		remaining[name] = true
	}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || !remaining[name] {
			return false
		}
		delete(remaining, name)
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	if _, err := d.Token(); err != nil || len(remaining) != 0 {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}
