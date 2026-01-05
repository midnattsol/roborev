package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CodexAgent runs code reviews using the Codex CLI
type CodexAgent struct {
	Command string // The codex command to run (default: "codex")
}

// NewCodexAgent creates a new Codex agent
func NewCodexAgent(command string) *CodexAgent {
	if command == "" {
		command = "codex"
	}
	return &CodexAgent{Command: command}
}

func (a *CodexAgent) Name() string {
	return "codex"
}

// reviewPrompt is the system prompt for code reviews
const reviewPrompt = `You are a code reviewer. Review the git commit shown below for:

1. **Bugs**: Logic errors, off-by-one errors, null/undefined issues, race conditions
2. **Security**: Injection vulnerabilities, auth issues, data exposure
3. **Regressions**: Changes that might break existing functionality

Focus on substantive issues only. Ignore style, formatting, and minor nitpicks.

If the commit looks good, say "No issues found" with a brief explanation.
If there are problems, list them concisely with file:line references where possible.

Review the most recent commit in this repository.`

func (a *CodexAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error) {
	// Create temp file for output
	tmpDir := os.TempDir()
	outputFile := filepath.Join(tmpDir, fmt.Sprintf("roborev-%s.txt", commitSHA[:8]))
	defer os.Remove(outputFile)

	// Use codex exec with output capture
	args := []string{
		"exec",
		"-C", repoPath,
		"-o", outputFile,
		"-c", `model_reasoning_effort="high"`,
		"--full-auto",
		fmt.Sprintf("%s\n\nCommit to review: %s", reviewPrompt, commitSHA),
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("codex failed: %w\nstderr: %s", err, stderr.String())
	}

	// Read the output file
	output, err := os.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}

	if len(output) == 0 {
		return "No review output generated", nil
	}

	return string(output), nil
}

func init() {
	Register(NewCodexAgent(""))
}
