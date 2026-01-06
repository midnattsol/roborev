package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
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

func (a *CodexAgent) CommandName() string {
	return a.Command
}

func (a *CodexAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error) {
	// Create unique temp file for output
	tmpFile, err := os.CreateTemp("", "roborev-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	outputFile := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(outputFile)

	// Use codex exec with output capture
	// The prompt is constructed by the prompt builder with full context
	args := []string{
		"exec",
		"-C", repoPath,
		"-o", outputFile,
		"-c", `model_reasoning_effort="high"`,
		"--full-auto",
		prompt,
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
