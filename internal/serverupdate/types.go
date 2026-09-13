package serverupdate

import (
	"context"
	"time"
)

type RuntimeStatus struct {
	API         int      `json:"api"`
	InstanceID  string   `json:"instance_id"`
	Version     string   `json:"version"`
	Commit      string   `json:"commit"`
	Schema      int      `json:"schema"`
	Ready       bool     `json:"ready"`
	Blocked     []string `json:"blocked"`
	Agents      []string `json:"agents"`
	Listeners   []int64  `json:"listeners"`
	Maintenance bool     `json:"maintenance"`
}

type Plan struct {
	InstanceID       string    `json:"instance_id"`
	CurrentVersion   string    `json:"current_version"`
	CurrentImage     string    `json:"current_image"`
	Release          Release   `json:"release"`
	DeploymentSHA256 string    `json:"deployment_sha256"`
	ExpiresAt        time.Time `json:"expires_at"`
	ID               string    `json:"id"`
}

type StartRequest struct {
	OperationID string `json:"operation_id"`
	Plan        Plan   `json:"plan"`
	Actor       string `json:"actor"`
}

type Operation struct {
	ID            string        `json:"id"`
	Plan          Plan          `json:"plan"`
	Actor         string        `json:"actor"`
	Phase         string        `json:"phase"`
	Detail        string        `json:"detail"`
	StartedAt     time.Time     `json:"started_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Baseline      RuntimeStatus `json:"baseline"`
	BackupSHA256  string        `json:"backup_sha256"`
	RecoveryPhase string        `json:"recovery_phase,omitempty"`
}

func (o Operation) Terminal() bool {
	return o.Phase == "succeeded" || o.Phase == "rolled_back" || o.Phase == "failed"
}

type Overview struct {
	InstanceID string        `json:"instance_id"`
	Channel    string        `json:"channel"`
	Current    RuntimeStatus `json:"current"`
	Target     *Release      `json:"target,omitempty"`
	Operation  *Operation    `json:"operation,omitempty"`
	Warning    string        `json:"warning"`
}

// Driver is implemented by a locally configured supervisor, never the manager.
type Driver interface {
	Inspect(context.Context) (RuntimeStatus, string, string, error)
	Pull(context.Context, string) error
	Gate(bool) error
	Prepare(context.Context) (RuntimeStatus, error)
	Stop(context.Context) error
	Backup(context.Context, string) (string, error)
	Restore(context.Context, string, string) error
	Deploy(context.Context, string) error
	Healthy(context.Context, Release, RuntimeStatus) error
	Commit(context.Context) error
}

type Source interface {
	Latest(context.Context, RuntimeStatus, Floor) (*Release, error)
	Resolve(context.Context, string, RuntimeStatus, Floor) (Release, error)
}
