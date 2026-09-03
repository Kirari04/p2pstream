package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"p2pstream/internal/updater"
)

// These narrow runners keep the command wiring testable. The production
// worker itself refuses to run until enrollment, pinned trust metadata, and
// durable replay counters have been provisioned atomically.
var (
	runUpdaterStage = func(ctx context.Context) error {
		worker, err := updater.NewWorker(updater.DefaultPaths())
		if err != nil {
			return err
		}
		return worker.Run(ctx)
	}
	runUpdaterActivate = func(ctx context.Context) error {
		return updater.RunActivator(ctx, updater.DefaultPaths())
	}
)

var updaterCmd = &cobra.Command{
	Use:          "updater",
	Short:        "Run managed host update lifecycle operations",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
}

var updaterStageCmd = &cobra.Command{
	Use:          "stage",
	Short:        "Check and stage a signed raw agent binary",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runUpdaterStage(cmd.Context())
	},
}

var updaterActivateCmd = &cobra.Command{
	Use:          "activate",
	Short:        "Offline-verify and activate a staged agent binary",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return errors.New("updater activation must run as root")
		}
		return runUpdaterActivate(cmd.Context())
	},
}

var updaterEnrollCmd = &cobra.Command{
	Use:          "enroll",
	Short:        "Enroll the isolated updater worker",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return errors.New("updater enrollment must run as the unprivileged p2pstream-updater user")
		}
		return updater.EnrollDefault(cmd.Context(), updater.DefaultPaths())
	},
}

var updaterFinalizeEnrollmentCmd = &cobra.Command{
	Use:          "finalize-enrollment",
	Short:        "Finalize verified enrollment and enable updater units",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return updater.FinalizeEnrollment(updater.DefaultPaths())
	},
}

var updaterBootstrapHostCmd = &cobra.Command{
	Use:          "bootstrap-host",
	Short:        "Create isolated updater identities and inert host state",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		channel := os.Getenv("P2PSTREAM_AGENT_UPDATE_CHANNEL")
		if channel == "" {
			channel = "stable"
		}
		origin, err := updater.ManagementOrigin(os.Getenv("MANAGEMENT_URL"))
		if err != nil {
			return err
		}
		trustedRoot, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("P2PSTREAM_AGENT_UPDATE_ROOT_BASE64"))
		if err != nil {
			return errors.New("P2PSTREAM_AGENT_UPDATE_ROOT_BASE64 is not canonical base64")
		}
		authorityPublicKey, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64"))
		if err != nil {
			return errors.New("P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64 is not canonical base64")
		}
		authorityEpoch, err := strconv.ParseUint(os.Getenv("P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH"), 10, 64)
		if err != nil || authorityEpoch == 0 {
			return errors.New("P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH must be a positive decimal integer")
		}
		identities, err := updater.BootstrapHost(updater.BootstrapOptions{
			Paths:       updater.DefaultPaths(),
			UpdaterUser: updater.DefaultUpdaterUser,
			Config: updater.HostConfig{
				Repository: os.Getenv("P2PSTREAM_REPOSITORY"), ManagementOrigin: origin,
				AgentPublicID: os.Getenv("AGENT_ID"), Channel: channel,
			},
			EnrollmentToken:       os.Getenv("P2PSTREAM_UPDATER_ENROLLMENT_TOKEN"),
			TrustedRootJSON:       trustedRoot,
			AuthorityPublicKey:    authorityPublicKey,
			AuthorityKeyID:        os.Getenv("P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID"),
			AuthorityEpoch:        authorityEpoch,
			CurrentVersion:        os.Getenv("P2PSTREAM_CURRENT_VERSION"),
			ExistingTunnelVersion: os.Getenv("P2PSTREAM_EXISTING_TUNNEL_VERSION"),
			ExistingTunnelCommit:  os.Getenv("P2PSTREAM_EXISTING_TUNNEL_COMMIT"),
			Reenroll:              os.Getenv("P2PSTREAM_UPDATER_REENROLL") == "true",
		})
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(encoded)); err != nil {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updaterCmd)
	updaterCmd.AddCommand(updaterStageCmd, updaterActivateCmd, updaterBootstrapHostCmd, updaterEnrollCmd, updaterFinalizeEnrollmentCmd)
}
