package updater

import (
	"fmt"
	"path/filepath"
)

func (p Paths) validate() error {
	values := map[string]string{
		"config": p.ConfigPath, "state": p.StateDir,
		"install root": p.InstallRoot, "command": p.CommandPath,
	}
	for name, value := range values {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("updater %s path must be a clean absolute path", name)
		}
	}
	return nil
}

func (p Paths) stagingDir() string   { return filepath.Join(p.StateDir, "staging") }
func (p Paths) rootStateDir() string { return filepath.Join(p.StateDir, "root") }
func (p Paths) candidateDir() string { return filepath.Join(p.stagingDir(), "candidate") }
func (p Paths) readyPath() string    { return filepath.Join(p.stagingDir(), "ready.json") }
func (p Paths) activationClaimPath() string {
	return filepath.Join(p.rootStateDir(), "activation-command.json")
}
func (p Paths) stagedPath() string  { return filepath.Join(p.stagingDir(), "staged.json") }
func (p Paths) floorPath() string   { return filepath.Join(p.StateDir, "floor.json") }
func (p Paths) journalPath() string { return filepath.Join(p.rootStateDir(), "activation.json") }
func (p Paths) lockPath() string    { return filepath.Join(p.rootStateDir(), "activation.lock") }
func (p Paths) slotsDir() string    { return filepath.Join(p.InstallRoot, "slots") }
func (p Paths) currentPath() string { return filepath.Join(p.InstallRoot, "current") }
