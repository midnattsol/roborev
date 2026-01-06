package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// ClaudeAgent runs code reviews using Claude Code CLI
type ClaudeAgent struct {
	Command string // The claude command to run (default: "claude")
}

// NewClaudeAgent creates a new Claude Code agent
func NewClaudeAgent(command string) *ClaudeAgent {
	if command == "" {
		command = "claude"
	}
	return &ClaudeAgent{Command: command}
}

func (a *ClaudeAgent) Name() string {
	return "claude-code"
}

func (a *ClaudeAgent) CommandName() string {
	return a.Command
}

func (a *ClaudeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error) {
	// Use claude CLI in print mode (non-interactive)
	// --print outputs the response without the interactive TUI
	args := []string{
		"--print",
		"-p", prompt,
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func init() {
	Register(NewClaudeAgent(""))
}
