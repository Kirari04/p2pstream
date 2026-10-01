package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Execute the workflow's actual gate after resolving its two GitHub contexts.
// Both inline expressions and step env are resolved, so putting a ref back into
// shell source would exercise the same expansion as a pull-request run.
func TestCIMainSourceGateTreatsBranchNamesAsData(t *testing.T) {
	contents, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	stepEnv := make(map[string]string)
	inStep, inRun, inEnv := false, false, false
	for _, line := range strings.Split(string(contents), "\n") {
		if line == "      - name: Require staging source for main" {
			inStep = true
			continue
		}
		if !inStep {
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "        ") {
			break
		}
		if line == "        run: |" {
			inRun, inEnv = true, false
			continue
		}
		if inRun {
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "          ") {
				break
			}
			script.WriteString(strings.TrimPrefix(line, "          ") + "\n")
			continue
		}
		if line == "        env:" {
			inEnv = true
			continue
		}
		if inEnv && strings.HasPrefix(line, "          ") {
			name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if !ok {
				t.Fatalf("invalid gate environment entry: %q", line)
			}
			stepEnv[name] = strings.TrimSpace(value)
		} else {
			inEnv = false
		}
	}
	if script.Len() == 0 {
		t.Fatal("main source gate script was not found")
	}

	for _, tc := range []struct {
		name, base, head string
		allowed          bool
	}{
		{"promotion", "main", "staging", true},
		{"wrong source", "main", "dev", false},
		{"development", "dev", "feature/change", true},
		{"staging", "staging", "dev", true},
		{"substitution", "main", "topic$(>gate-marker)", false},
		{"backticks", "main", "topic`>gate-marker`", false},
		{"development substitution", "dev", "topic$(>gate-marker)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if output, err := exec.Command("git", "check-ref-format", "--branch", tc.head).CombinedOutput(); err != nil {
				t.Fatalf("test branch is not a valid Git ref: %v: %s", err, output)
			}
			expand := strings.NewReplacer("${{ github.base_ref }}", tc.base, "${{ github.head_ref }}", tc.head)
			cmd := exec.Command("bash", "-c", expand.Replace(script.String()))
			cmd.Dir = t.TempDir()
			cmd.Env = os.Environ()
			for name, value := range stepEnv {
				cmd.Env = append(cmd.Env, name+"="+expand.Replace(value))
			}
			output, err := cmd.CombinedOutput()
			if _, markerErr := os.Stat(cmd.Dir + "/gate-marker"); !os.IsNotExist(markerErr) {
				t.Fatal("branch name executed shell syntax")
			}
			if (err == nil) != tc.allowed {
				t.Fatalf("gate allowed=%v, want %v: %v: %s", err == nil, tc.allowed, err, output)
			}
		})
	}
}
