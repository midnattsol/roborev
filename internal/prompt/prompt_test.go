package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// setupTestRepo creates a git repo with multiple commits and returns the repo path and commit SHAs
func setupTestRepo(t *testing.T) (string, []string) {
	t.Helper()
	tmpDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	var commits []string

	// Create 6 commits so we can test with 5 previous commits
	for i := 1; i <= 6; i++ {
		filename := filepath.Join(tmpDir, "file.txt")
		content := strings.Repeat("x", i) // Different content each time
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "commit "+string(rune('0'+i)))

		sha := runGit("rev-parse", "HEAD")
		commits = append(commits, sha)
	}

	return tmpDir, commits
}

func TestBuildPromptWithoutContext(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt without database (no previous reviews)
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain system prompt
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}

	// Should contain the 5 review criteria
	expectedCriteria := []string{"Bugs", "Security", "Testing gaps", "Regressions", "Code quality"}
	for _, criteria := range expectedCriteria {
		if !strings.Contains(prompt, criteria) {
			t.Errorf("Prompt should contain %q", criteria)
		}
	}

	// Should contain current commit section
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}

	// Should contain short SHA
	shortSHA := targetSHA[:7]
	if !strings.Contains(prompt, shortSHA) {
		t.Errorf("Prompt should contain short SHA %s", shortSHA)
	}

	// Should NOT contain previous reviews section (no db)
	if strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should not contain previous reviews section without db")
	}
}

func TestBuildPromptWithPreviousReviews(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	db := testutil.OpenTestDB(t)

	// Create repo and commits in DB
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create reviews for commits 2, 3, and 4 (leaving 1 and 5 without reviews)
	reviewTexts := map[int]string{
		1: "Review for commit 2: Found a bug in error handling",
		2: "Review for commit 3: No issues found",
		3: "Review for commit 4: Security issue - missing input validation",
	}

	for i, sha := range commits[:5] { // First 5 commits (parents of commit 6)
		// Ensure commit exists in DB
		if _, err := db.GetOrCreateCommit(repo.ID, sha, "Test", "commit message", time.Now()); err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}

		// Create review for some commits
		if reviewText, ok := reviewTexts[i]; ok {
			testutil.CreateCompletedReview(t, db, repo.ID, sha, "test", reviewText)
		}
	}

	// Also add commit 6 to DB (the target commit)
	_, err = db.GetOrCreateCommit(repo.ID, commits[5], "Test", "commit message", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Build prompt with 5 previous commits context
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 5, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous reviews section
	if !strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should contain previous reviews section")
	}

	// Should contain the review texts we added
	for _, reviewText := range reviewTexts {
		if !strings.Contains(prompt, reviewText) {
			t.Errorf("Prompt should contain review text: %s", reviewText)
		}
	}

	// Should contain "No review available" for commits without reviews
	if !strings.Contains(prompt, "No review available") {
		t.Error("Prompt should contain 'No review available' for commits without reviews")
	}

	// Should contain delimiters with short SHAs
	if !strings.Contains(prompt, "--- Review for commit") {
		t.Error("Prompt should contain review delimiters")
	}

	// Verify chronological order (oldest first)
	// The oldest parent (commit 1) should appear before the newest parent (commit 5)
	commit1Pos := strings.Index(prompt, commits[0][:7])
	commit5Pos := strings.Index(prompt, commits[4][:7])
	if commit1Pos == -1 || commit5Pos == -1 {
		t.Error("Prompt should contain short SHAs of parent commits")
	} else if commit1Pos > commit5Pos {
		t.Error("Commits should be in chronological order (oldest first)")
	}
}

func TestBuildPromptWithPreviousReviewsAndResponses(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	db := testutil.OpenTestDB(t)

	// Create repo
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create review for commit 3 (parent of commit 6) with responses
	parentSHA := commits[2] // commit 3
	testutil.CreateReviewWithComments(t, db, repo.ID, parentSHA,
		"Found potential memory leak in connection pool",
		[]testutil.ReviewComment{
			{User: "alice", Text: "Known issue, will fix in next sprint"},
			{User: "bob", Text: "Added to tech debt backlog"},
		})

	// Also add commits 4 and 5 to DB
	for _, sha := range commits[3:5] {
		db.GetOrCreateCommit(repo.ID, sha, "Test", "commit", time.Now())
	}

	// Build prompt for commit 6 with context from previous 5 commits
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 5, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous reviews section
	if !strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should contain previous reviews section")
	}

	// Should contain the review text
	if !strings.Contains(prompt, "memory leak in connection pool") {
		t.Error("Prompt should contain the previous review text")
	}

	// Should contain comments on the previous review
	if !strings.Contains(prompt, "Comments on this review:") {
		t.Error("Prompt should contain comments section for previous review")
	}

	if !strings.Contains(prompt, "alice") {
		t.Error("Prompt should contain commenter 'alice'")
	}

	if !strings.Contains(prompt, "Known issue, will fix in next sprint") {
		t.Error("Prompt should contain alice's comment text")
	}

	if !strings.Contains(prompt, "bob") {
		t.Error("Prompt should contain commenter 'bob'")
	}

	if !strings.Contains(prompt, "Added to tech debt backlog") {
		t.Error("Prompt should contain bob's comment text")
	}
}

func TestBuildPromptWithNoParentCommits(t *testing.T) {
	// Create a repo with just one commit
	tmpDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")
	sha := runGit("rev-parse", "HEAD")

	db := testutil.OpenTestDB(t)

	// Build prompt - should work even with no parent commits
	builder := NewBuilder(db)
	prompt, err := builder.Build(tmpDir, sha, 0, 5, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain system prompt and current commit
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}

	// Should NOT contain previous reviews (no parents exist)
	if strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should not contain previous reviews section when no parents exist")
	}
}

func TestPromptContainsExpectedFormat(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	db := testutil.OpenTestDB(t)

	repo, _ := db.GetOrCreateRepo(repoPath)
	testutil.CreateCompletedReview(t, db, repo.ID, commits[4], "test", "Found 1 issue:\n1. pkg/cache/store.go:112 - Race condition")

	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 3, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Print the prompt for visual inspection
	t.Logf("Generated prompt:\n%s", prompt)

	// Verify structure
	sections := []string{
		"You are a code reviewer",
		"## Previous Reviews",
		"--- Review for commit",
		"## Current Commit",
	}

	for _, section := range sections {
		if !strings.Contains(prompt, section) {
			t.Errorf("Prompt missing section: %s", section)
		}
	}
}

func TestBuildPromptWithProjectGuidelines(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with review guidelines as multi-line string
	configContent := `
agent = "codex"
review_guidelines = """
We are not doing database migrations because there are no production databases yet.
Prefer composition over inheritance.
All public APIs must have documentation comments.
"""
`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt without database
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain project guidelines section
	if !strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should contain project guidelines section")
	}

	// Should contain the guidelines text
	if !strings.Contains(prompt, "database migrations") {
		t.Error("Prompt should contain guidelines about database migrations")
	}
	if !strings.Contains(prompt, "composition over inheritance") {
		t.Error("Prompt should contain guidelines about composition")
	}
	if !strings.Contains(prompt, "documentation comments") {
		t.Error("Prompt should contain guidelines about documentation")
	}

	// Print prompt for inspection
	t.Logf("Generated prompt with guidelines:\n%s", prompt)
}

func TestBuildPromptWithoutProjectGuidelines(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml WITHOUT review guidelines
	configContent := `agent = "codex"`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should NOT contain project guidelines section
	if strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should not contain project guidelines section when none configured")
	}
}

func TestBuildPromptNoConfig(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should NOT contain project guidelines section
	if strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should not contain project guidelines section when no config file")
	}

	// Should still contain standard sections
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}
}

func TestBuildPromptGuidelinesOrder(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with review guidelines
	configContent := `review_guidelines = "Test guideline"`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Guidelines should appear after system prompt but before current commit
	systemPromptPos := strings.Index(prompt, "You are a code reviewer")
	guidelinesPos := strings.Index(prompt, "## Project Guidelines")
	currentCommitPos := strings.Index(prompt, "## Current Commit")

	if guidelinesPos == -1 {
		t.Fatal("Guidelines section not found")
	}

	if systemPromptPos > guidelinesPos {
		t.Error("System prompt should come before guidelines")
	}

	if guidelinesPos > currentCommitPos {
		t.Error("Guidelines should come before current commit section")
	}
}

func TestBuildPromptWithPreviousAttempts(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[5] // Last commit

	db := testutil.OpenTestDB(t)

	// Create repo and commit in DB
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create two previous reviews for the SAME commit (simulating re-reviews)
	reviewTexts := []string{
		"First review: Found missing error handling",
		"Second review: Still missing error handling, also found SQL injection",
	}

	for _, reviewText := range reviewTexts {
		testutil.CreateCompletedReview(t, db, repo.ID, targetSHA, "test", reviewText)
	}

	// Build prompt - should include previous attempts for the same commit
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, targetSHA, repo.ID, 0, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous review attempts section
	if !strings.Contains(prompt, "## Previous Review Attempts") {
		t.Error("Prompt should contain previous review attempts section")
	}

	// Should contain both review texts
	for _, reviewText := range reviewTexts {
		if !strings.Contains(prompt, reviewText) {
			t.Errorf("Prompt should contain review text: %s", reviewText)
		}
	}

	// Should contain attempt numbers
	if !strings.Contains(prompt, "Review Attempt 1") {
		t.Error("Prompt should contain 'Review Attempt 1'")
	}
	if !strings.Contains(prompt, "Review Attempt 2") {
		t.Error("Prompt should contain 'Review Attempt 2'")
	}
}

func TestBuildPromptWithPreviousAttemptsAndResponses(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[5]

	db := testutil.OpenTestDB(t)

	repo, _ := db.GetOrCreateRepo(repoPath)

	// Create a previous review with a comment
	testutil.CreateReviewWithComments(t, db, repo.ID, targetSHA,
		"Found issue: missing null check",
		[]testutil.ReviewComment{
			{User: "developer", Text: "This is intentional, the value is never null here"},
		})

	// Build prompt for a new review of the same commit
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, targetSHA, repo.ID, 0, "", "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain the previous review
	if !strings.Contains(prompt, "## Previous Review Attempts") {
		t.Error("Prompt should contain previous review attempts section")
	}

	if !strings.Contains(prompt, "missing null check") {
		t.Error("Prompt should contain the previous review text")
	}

	// Should contain the comment
	if !strings.Contains(prompt, "Comments on this review:") {
		t.Error("Prompt should contain comments section")
	}

	if !strings.Contains(prompt, "This is intentional") {
		t.Error("Prompt should contain the comment text")
	}

	if !strings.Contains(prompt, "developer") {
		t.Error("Prompt should contain the commenter name")
	}
}

func TestBuildPromptWithGeminiAgent(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt for Gemini agent
	prompt, err := BuildSimple(repoPath, targetSHA, "gemini")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain Gemini-specific instructions
	if !strings.Contains(prompt, "extremely concise and professional") {
		t.Error("Prompt should contain Gemini-specific instruction")
	}
	if !strings.Contains(prompt, "Summary") {
		t.Error("Prompt should contain 'Summary' section")
	}

	// Should NOT contain default system prompt text
	if strings.Contains(prompt, "Brief explanation of the problem and suggested fix") {
		t.Error("Prompt should not contain default system prompt text")
	}
}

func TestBuildPromptDesignReviewType(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Single commit with reviewType="design" should use design-review prompt
	b := NewBuilder(nil)
	prompt, err := b.Build(repoPath, targetSHA, 0, 0, "test", "design")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !strings.Contains(prompt, "design reviewer") {
		t.Error("Expected design-review system prompt for reviewType=design")
	}
	if strings.Contains(prompt, "code reviewer") {
		t.Error("Should not contain standard code review prompt")
	}
}

func TestBuildDirtyDesignReviewType(t *testing.T) {
	diff := "diff --git a/design.md b/design.md\n+# Design\n"
	b := NewBuilder(nil)

	// Use a temp dir as repo path (no .roborev.toml needed)
	repoPath := t.TempDir()
	prompt, err := b.BuildDirty(repoPath, diff, 0, 0, "test", "design")
	if err != nil {
		t.Fatalf("BuildDirty failed: %v", err)
	}
	if !strings.Contains(prompt, "design reviewer") {
		t.Error("Expected design-review system prompt for dirty reviewType=design")
	}
	if strings.Contains(prompt, "code reviewer") {
		t.Error("Should not contain standard dirty review prompt")
	}
}

func TestBuildDirtyWithReviewAlias(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n+func foo() {}\n"
	b := NewBuilder(nil)
	repoPath := t.TempDir()

	// "review" alias should produce the dirty prompt, NOT the single-commit prompt
	prompt, err := b.BuildDirty(repoPath, diff, 0, 0, "test", "review")
	if err != nil {
		t.Fatalf("BuildDirty failed: %v", err)
	}
	if !strings.Contains(prompt, "uncommitted changes") {
		t.Error("Expected dirty system prompt for reviewType=review alias, got wrong prompt type")
	}
}

func TestBuildRangeWithReviewAlias(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	// Use a two-commit range
	rangeRef := commits[3] + ".." + commits[5]

	b := NewBuilder(nil)
	prompt, err := b.Build(repoPath, rangeRef, 0, 0, "test", "review")
	if err != nil {
		t.Fatalf("Build (range) failed: %v", err)
	}
	// Should use the range system prompt, not single-commit
	if !strings.Contains(prompt, "commit range") {
		t.Error("Expected range system prompt for reviewType=review alias, got wrong prompt type")
	}
}

func TestBuildPromptWithContextFiles(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create context files
	docsDir := filepath.Join(repoPath, "docs", "adr")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("Failed to create docs dir: %v", err)
	}

	adr1 := filepath.Join(docsDir, "001-use-go.md")
	if err := os.WriteFile(adr1, []byte("# ADR 001: Use Go\n\nWe chose Go for performance."), 0644); err != nil {
		t.Fatalf("Failed to write ADR: %v", err)
	}

	adr2 := filepath.Join(docsDir, "002-rest-api.md")
	if err := os.WriteFile(adr2, []byte("# ADR 002: REST API\n\nWe use REST for simplicity."), 0644); err != nil {
		t.Fatalf("Failed to write ADR: %v", err)
	}

	// Create .roborev.toml with context_files
	configContent := `context_files = ["docs/adr/*.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section")
	}

	if !strings.Contains(prompt, "ADR 001: Use Go") {
		t.Error("Prompt should contain ADR 001 content")
	}

	if !strings.Contains(prompt, "ADR 002: REST API") {
		t.Error("Prompt should contain ADR 002 content")
	}
}

func TestBuildPromptWithContextFilesOrder(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a context file
	archFile := filepath.Join(repoPath, "ARCHITECTURE.md")
	if err := os.WriteFile(archFile, []byte("# Architecture\n\nHexagonal architecture."), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create .roborev.toml with both guidelines and context_files
	configContent := `
review_guidelines = "Be thorough"
context_files = ["ARCHITECTURE.md"]
`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	guidelinesPos := strings.Index(prompt, "## Project Guidelines")
	contextFilesPos := strings.Index(prompt, "## Context Files")
	currentCommitPos := strings.Index(prompt, "## Current Commit")

	if guidelinesPos == -1 {
		t.Fatal("Guidelines section not found")
	}
	if contextFilesPos == -1 {
		t.Fatal("Context files section not found")
	}

	if guidelinesPos > contextFilesPos {
		t.Error("Guidelines should come before context files")
	}
	if contextFilesPos > currentCommitPos {
		t.Error("Context files should come before current commit")
	}
}

func TestBuildPromptWithMissingContextFile(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with a non-existent file
	configContent := `context_files = ["DOES_NOT_EXIST.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Should not fail, just skip the missing file
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail with missing context file: %v", err)
	}

	// Should not contain context files section since file was skipped
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section when all files are missing")
	}

	// Should still have the standard sections
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should still contain current commit section")
	}
}

func TestBuildPromptWithEmptyGlobMatch(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with a glob that matches nothing
	configContent := `context_files = ["nonexistent/*.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Should not fail
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail with empty glob: %v", err)
	}

	// Should not contain context files section
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section when glob matches nothing")
	}
}

func TestBuildPromptWithPathTraversal(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file outside repo (in parent dir)
	parentDir := filepath.Dir(repoPath)
	secretFile := filepath.Join(parentDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("SECRET DATA"), 0644); err != nil {
		t.Fatalf("Failed to write secret file: %v", err)
	}
	defer os.Remove(secretFile)

	// Create .roborev.toml with path traversal attempt
	configContent := `context_files = ["../secret.txt"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should NOT contain the secret file content
	if strings.Contains(prompt, "SECRET DATA") {
		t.Error("Prompt should not contain content from files outside repo")
	}

	// Should NOT contain context files section
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section when all files are outside repo")
	}
}

func TestBuildPromptWithDeduplication(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file
	archFile := filepath.Join(repoPath, "ARCHITECTURE.md")
	if err := os.WriteFile(archFile, []byte("# Architecture\n\nUnique content here."), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create .roborev.toml with same file via glob and explicit path
	configContent := `context_files = ["*.md", "ARCHITECTURE.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Count occurrences of the unique content - should appear only once
	count := strings.Count(prompt, "Unique content here.")
	if count != 1 {
		t.Errorf("Expected file content to appear once (deduplicated), got %d occurrences", count)
	}
}

func TestBuildPromptWithLargeContextTruncation(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a large file that exceeds budget
	largeContent := strings.Repeat("x", 100*1024) // 100KB
	largeFile := filepath.Join(repoPath, "large.md")
	if err := os.WriteFile(largeFile, []byte(largeContent), 0644); err != nil {
		t.Fatalf("Failed to write large file: %v", err)
	}

	// Create another file that won't fit
	secondFile := filepath.Join(repoPath, "second.md")
	if err := os.WriteFile(secondFile, []byte("Second file content"), 0644); err != nil {
		t.Fatalf("Failed to write second file: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["large.md", "second.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain truncation message
	if !strings.Contains(prompt, "context truncated") {
		t.Error("Prompt should contain truncation message when context is too large")
	}

	// Should still have standard sections
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should still contain current commit section")
	}
}

func TestBuildPromptWithSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlink test not supported on Windows")
	}

	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file outside repo
	parentDir := filepath.Dir(repoPath)
	secretFile := filepath.Join(parentDir, "external_secret.txt")
	if err := os.WriteFile(secretFile, []byte("EXTERNAL SECRET DATA"), 0644); err != nil {
		t.Fatalf("Failed to write external file: %v", err)
	}
	defer os.Remove(secretFile)

	// Create symlink inside repo pointing to the external file
	symlinkPath := filepath.Join(repoPath, "link_to_secret.txt")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	// Create .roborev.toml referencing the symlink
	configContent := `context_files = ["link_to_secret.txt"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should NOT contain the external file content
	if strings.Contains(prompt, "EXTERNAL SECRET DATA") {
		t.Error("Prompt should not contain content from symlinks pointing outside repo")
	}

	// Should NOT contain context files section since symlink was rejected
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section when symlink points outside")
	}
}

func TestBuildPromptWithOversizedFileBoundedRead(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a very large file (larger than context budget)
	largeContent := strings.Repeat("LARGE_FILE_CONTENT_", 50000) // ~950KB
	largeFile := filepath.Join(repoPath, "huge.md")
	if err := os.WriteFile(largeFile, []byte(largeContent), 0644); err != nil {
		t.Fatalf("Failed to write large file: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["huge.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Should not panic or OOM
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should still have standard sections
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should still contain current commit section")
	}

	// File content should be truncated (not all of it present)
	if strings.Contains(prompt, largeContent) {
		t.Error("Prompt should not contain full oversized file content")
	}
}

func TestBuildPromptWithBrokenSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlink test not supported on Windows")
	}

	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a symlink to a non-existent file
	brokenLink := filepath.Join(repoPath, "broken_link.md")
	if err := os.Symlink("/nonexistent/path/file.txt", brokenLink); err != nil {
		t.Fatalf("Failed to create broken symlink: %v", err)
	}

	// Create a valid file to include
	validFile := filepath.Join(repoPath, "valid.md")
	if err := os.WriteFile(validFile, []byte("VALID_CONTENT"), 0644); err != nil {
		t.Fatalf("Failed to write valid file: %v", err)
	}

	// Create .roborev.toml referencing both
	configContent := `context_files = ["broken_link.md", "valid.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should NOT contain broken symlink but SHOULD contain valid file
	if !strings.Contains(prompt, "VALID_CONTENT") {
		t.Error("Prompt should contain valid file content")
	}

	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section for valid file")
	}
}

func TestBuildPromptWithDirectoryInContextFiles(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a directory (which should be rejected)
	subDir := filepath.Join(repoPath, "docs")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Create a valid file
	validFile := filepath.Join(repoPath, "valid.md")
	if err := os.WriteFile(validFile, []byte("VALID_DOC"), 0644); err != nil {
		t.Fatalf("Failed to write valid file: %v", err)
	}

	// Create .roborev.toml referencing directory and file
	configContent := `context_files = ["docs", "valid.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should contain valid file but not directory listing
	if !strings.Contains(prompt, "VALID_DOC") {
		t.Error("Prompt should contain valid file content")
	}
}

func TestBuildPromptWithSymlinkChainEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlink test not supported on Windows")
	}

	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file outside repo
	parentDir := filepath.Dir(repoPath)
	secretFile := filepath.Join(parentDir, "chain_secret.txt")
	if err := os.WriteFile(secretFile, []byte("CHAIN_SECRET_DATA"), 0644); err != nil {
		t.Fatalf("Failed to write external file: %v", err)
	}
	defer os.Remove(secretFile)

	// Create a chain of symlinks: link1 -> link2 -> external file
	link2 := filepath.Join(repoPath, "link2.txt")
	if err := os.Symlink(secretFile, link2); err != nil {
		t.Fatalf("Failed to create link2: %v", err)
	}

	link1 := filepath.Join(repoPath, "link1.txt")
	if err := os.Symlink(link2, link1); err != nil {
		t.Fatalf("Failed to create link1: %v", err)
	}

	// Create .roborev.toml referencing the first link in the chain
	configContent := `context_files = ["link1.txt"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should NOT contain the external file content (chain resolves outside repo)
	if strings.Contains(prompt, "CHAIN_SECRET_DATA") {
		t.Error("Prompt should not contain content from symlink chain pointing outside repo")
	}

	// Should NOT contain context files section since symlink was rejected
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section when symlink chain points outside")
	}
}

func TestBuildPromptWithSymlinkedRepoRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlink test not supported on Windows")
	}

	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a context file in the real repo
	contextFile := filepath.Join(repoPath, "context.md")
	if err := os.WriteFile(contextFile, []byte("SYMLINKED_REPO_CONTEXT"), 0644); err != nil {
		t.Fatalf("Failed to write context file: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["context.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Create a symlink to the repo in the parent directory
	parentDir := filepath.Dir(repoPath)
	symlinkRepo := filepath.Join(parentDir, "symlinked_repo")
	if err := os.Symlink(repoPath, symlinkRepo); err != nil {
		t.Fatalf("Failed to create symlink to repo: %v", err)
	}
	defer os.Remove(symlinkRepo)

	// Build prompt using the symlinked path
	prompt, err := BuildSimple(symlinkRepo, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail with symlinked repo path: %v", err)
	}

	// Should contain the context file content even when accessed via symlink
	if !strings.Contains(prompt, "SYMLINKED_REPO_CONTEXT") {
		t.Error("Prompt should contain context file content when repo is accessed via symlink")
	}

	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section when accessed via symlinked repo")
	}
}

func TestBuildPromptWithDotDotFilename(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file with .. prefix in name (valid filename, not traversal)
	dotDotFile := filepath.Join(repoPath, "..notes.md")
	if err := os.WriteFile(dotDotFile, []byte("DOTDOT_FILENAME_CONTENT"), 0644); err != nil {
		t.Fatalf("Failed to write ..notes.md file: %v", err)
	}

	// Create .roborev.toml referencing this file
	configContent := `context_files = ["..notes.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should contain the file content (it's a valid in-repo file, not traversal)
	if !strings.Contains(prompt, "DOTDOT_FILENAME_CONTENT") {
		t.Error("Prompt should contain content from ..notes.md (valid filename, not traversal)")
	}

	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section for ..notes.md")
	}
}

func TestBuildPromptWithAbsolutePathInConfig(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with absolute path style entry
	// Note: filepath.Join(repoAbs, "/etc/passwd") normalizes to <repoAbs>/etc/passwd
	// so this tests that such a non-existent in-repo path is handled gracefully
	configContent := `context_files = ["/etc/passwd"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple should not fail: %v", err)
	}

	// Should NOT contain /etc/passwd content (file doesn't exist at <repo>/etc/passwd)
	if strings.Contains(prompt, "root:") {
		t.Error("Prompt should not contain /etc/passwd content")
	}

	// No context files section since file doesn't exist
	if strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should not contain context files section for non-existent path")
	}
}

func TestIsInsideRepo(t *testing.T) {
	repoAbs := "/home/user/repo"

	tests := []struct {
		name       string
		targetPath string
		want       bool
	}{
		{"absolute path outside repo", "/etc/passwd", false},
		{"absolute path inside repo", "/home/user/repo/file.md", true},
		{"relative path inside repo", "/home/user/repo/docs/adr.md", true},
		{"traversal attempt", "/home/user/repo/../other/file.md", false},
		{"parent directory", "/home/user", false},
		{"sibling directory", "/home/user/other-repo/file.md", false},
		{"dotdot filename inside repo", "/home/user/repo/..notes.md", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInsideRepo(repoAbs, tt.targetPath)
			if got != tt.want {
				t.Errorf("isInsideRepo(%q, %q) = %v, want %v", repoAbs, tt.targetPath, got, tt.want)
			}
		})
	}
}

func TestBuildPromptWithSymlinkDeduplication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlink test not supported on Windows")
	}

	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a real file
	realFile := filepath.Join(repoPath, "real.md")
	if err := os.WriteFile(realFile, []byte("DEDUP_CONTENT"), 0644); err != nil {
		t.Fatalf("Failed to write real file: %v", err)
	}

	// Create a symlink to the same file within the repo
	symlinkFile := filepath.Join(repoPath, "link.md")
	if err := os.Symlink(realFile, symlinkFile); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	// Create .roborev.toml referencing both (should dedupe)
	configContent := `context_files = ["real.md", "link.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Content should appear exactly once (deduplicated by resolved path)
	count := strings.Count(prompt, "DEDUP_CONTENT")
	if count != 1 {
		t.Errorf("Expected content to appear once (deduplicated), got %d occurrences", count)
	}
}

func TestBuildPromptWithBackticksInContextFile(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file with triple backticks that could break fencing
	contentWithBackticks := "# Example\n\n```go\nfunc main() {}\n```\n\nMore text after code block."
	backtickFile := filepath.Join(repoPath, "example.md")
	if err := os.WriteFile(backtickFile, []byte(contentWithBackticks), 0644); err != nil {
		t.Fatalf("Failed to write file with backticks: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["example.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain the full content including the inner backticks
	if !strings.Contains(prompt, "```go") {
		t.Error("Prompt should contain the inner code fence from context file")
	}
	if !strings.Contains(prompt, "More text after code block") {
		t.Error("Prompt should contain text after inner code fence")
	}

	// Should use a longer fence (4+ backticks) to encapsulate the content
	if !strings.Contains(prompt, "````") {
		t.Error("Prompt should use extended fence (4+ backticks) when content contains triple backticks")
	}

	// The content should be properly encapsulated (verify structure)
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section")
	}
}

func TestFenceForContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantOk  bool
		wantLen int // expected fence length if ok
	}{
		{"no backticks", "hello world", true, 3},
		{"single backtick", "hello `world`", true, 3},
		{"triple backticks", "```code```", true, 4},
		{"9 backticks", "`````````", true, 10},
		{"10 backticks (at limit)", "``````````", false, 0},
		{"many backticks", strings.Repeat("`", 50), false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fence, ok := fenceForContent(tt.content)
			if ok != tt.wantOk {
				t.Errorf("fenceForContent() ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && len(fence) != tt.wantLen {
				t.Errorf("fenceForContent() fence length = %d, want %d", len(fence), tt.wantLen)
			}
		})
	}
}

func TestBuildPromptWithUnfenceableContent(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file with too many consecutive backticks (unfenceable)
	unfenceableContent := strings.Repeat("`", 15) + "\nSome content"
	unfenceableFile := filepath.Join(repoPath, "unfenceable.md")
	if err := os.WriteFile(unfenceableFile, []byte(unfenceableContent), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create a normal file
	normalFile := filepath.Join(repoPath, "normal.md")
	if err := os.WriteFile(normalFile, []byte("Normal content"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create .roborev.toml with both files
	configContent := `context_files = ["unfenceable.md", "normal.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should NOT contain the unfenceable file content
	if strings.Contains(prompt, strings.Repeat("`", 15)) {
		t.Error("Prompt should not contain unfenceable file content")
	}

	// Should contain the normal file content
	if !strings.Contains(prompt, "Normal content") {
		t.Error("Prompt should contain normal file content")
	}

	// Should have context files section (for the normal file)
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section")
	}
}

func TestSanitizeDisplayPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"normal path", "normal/path.md", "normal/path.md"},
		{"newlines", "path\nwith\nnewlines.md", "path_with_newlines.md"},
		{"carriage returns", "path\rwith\rcarriage.md", "path_with_carriage.md"},
		{"tabs", "path\twith\ttabs.md", "path_with_tabs.md"},
		{"null and control", "path\x00with\x1fnull.md", "path_with_null.md"},
		{"DEL char", "path\x7fwith\x7fdel.md", "path_with_del.md"},
		{"empty", "", ""},
		{"traversal dots ok", "../traversal/attempt", "../traversal/attempt"},
		// Unicode control characters
		{"unicode line separator", "path\u2028sep.md", "path_sep.md"},
		{"unicode paragraph separator", "path\u2029sep.md", "path_sep.md"},
		// Bidi formatting characters
		{"bidi LRE", "path\u202Abidi.md", "path_bidi.md"},
		{"bidi RLE", "path\u202Bbidi.md", "path_bidi.md"},
		{"bidi PDF", "path\u202Cbidi.md", "path_bidi.md"},
		{"bidi LRO", "path\u202Dbidi.md", "path_bidi.md"},
		{"bidi RLO", "path\u202Ebidi.md", "path_bidi.md"},
		{"bidi LRI", "path\u2066bidi.md", "path_bidi.md"},
		{"bidi RLI", "path\u2067bidi.md", "path_bidi.md"},
		{"bidi FSI", "path\u2068bidi.md", "path_bidi.md"},
		{"bidi PDI", "path\u2069bidi.md", "path_bidi.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeDisplayPath(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeDisplayPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeDisplayPathTruncation(t *testing.T) {
	// Create a very long path
	longPath := strings.Repeat("a/", 300) + "file.md"

	result := sanitizeDisplayPath(longPath)

	// Should be truncated to maxDisplayPathLength + "..."
	if len(result) > maxDisplayPathLength+3 {
		t.Errorf("sanitizeDisplayPath should truncate long paths, got length %d", len(result))
	}

	if !strings.HasSuffix(result, "...") {
		t.Error("Truncated path should end with '...'")
	}
}

func TestBuildPromptWithVeryLongPath(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a deeply nested directory structure with long names
	deepPath := repoPath
	for i := 0; i < 20; i++ {
		deepPath = filepath.Join(deepPath, fmt.Sprintf("very_long_directory_name_%d", i))
	}
	if err := os.MkdirAll(deepPath, 0755); err != nil {
		t.Fatalf("Failed to create deep directory: %v", err)
	}

	// Create a file with a long name in the deep directory
	longFileName := strings.Repeat("long_", 20) + "file.md"
	longFile := filepath.Join(deepPath, longFileName)
	if err := os.WriteFile(longFile, []byte("Content in deeply nested file"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Get relative path for config
	relPath, _ := filepath.Rel(repoPath, longFile)

	// Create .roborev.toml
	configContent := fmt.Sprintf(`context_files = [%q]`, relPath)
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain the file content
	if !strings.Contains(prompt, "Content in deeply nested file") {
		t.Error("Prompt should contain content from deeply nested file")
	}

	// Context section should not exceed a reasonable size
	// (this is a sanity check - exact budget enforcement is tested elsewhere)
	contextStart := strings.Index(prompt, "## Context Files")
	if contextStart != -1 {
		contextSection := prompt[contextStart:]
		// Context budget is MaxPromptSize/4 ≈ 62KB, should be well under
		if len(contextSection) > MaxPromptSize/4+1000 { // allow small margin for truncation message
			t.Errorf("Context section too large: %d bytes", len(contextSection))
		}
	}
}

func TestBuildPromptWithContextFilesHeading(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a simple context file
	contextFile := filepath.Join(repoPath, "doc.md")
	if err := os.WriteFile(contextFile, []byte("Content here"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["doc.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should use plain text heading (path is sanitized, no backticks needed)
	if !strings.Contains(prompt, "### doc.md") {
		t.Error("Prompt should contain plain text heading for context file")
	}
}

func TestBuildPromptWithBackticksInFilename(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create a file with backticks in the name (valid on Unix)
	// Note: This tests that backticks in filenames don't break prompt structure
	backtickFile := filepath.Join(repoPath, "file`with`backticks.md")
	if err := os.WriteFile(backtickFile, []byte("Content with backticks in filename"), 0644); err != nil {
		// Some filesystems may not allow this - skip test if so
		t.Skipf("Cannot create file with backticks: %v", err)
	}

	// Create .roborev.toml
	configContent := `context_files = ["file` + "`with`" + `backticks.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain the file content (proves file was processed)
	if !strings.Contains(prompt, "Content with backticks in filename") {
		t.Error("Prompt should contain content from file with backticks in name")
	}

	// Should have context files section
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Prompt should contain context files section")
	}
}

func TestBuildDirtyWithContextFiles(t *testing.T) {
	repoPath := t.TempDir()

	// Create a context file
	contextFile := filepath.Join(repoPath, "ARCHITECTURE.md")
	if err := os.WriteFile(contextFile, []byte("# Architecture\n\nDirty context test."), 0644); err != nil {
		t.Fatalf("Failed to write context file: %v", err)
	}

	// Create .roborev.toml with context_files
	configContent := `context_files = ["ARCHITECTURE.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	diff := "diff --git a/foo.go b/foo.go\n+func foo() {}\n"
	b := NewBuilder(nil)

	prompt, err := b.BuildDirty(repoPath, diff, 0, 0, "test", "")
	if err != nil {
		t.Fatalf("BuildDirty failed: %v", err)
	}

	// Should contain context files section
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("BuildDirty prompt should contain context files section")
	}

	if !strings.Contains(prompt, "Dirty context test") {
		t.Error("BuildDirty prompt should contain context file content")
	}

	// Should still have dirty-specific sections
	if !strings.Contains(prompt, "## Uncommitted Changes") {
		t.Error("BuildDirty prompt should contain uncommitted changes section")
	}
}

func TestBuildRangeWithContextFiles(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	rangeRef := commits[3] + ".." + commits[5]

	// Create a context file
	contextFile := filepath.Join(repoPath, "GUIDELINES.md")
	if err := os.WriteFile(contextFile, []byte("# Guidelines\n\nRange context test."), 0644); err != nil {
		t.Fatalf("Failed to write context file: %v", err)
	}

	// Create .roborev.toml with context_files
	configContent := `context_files = ["GUIDELINES.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	b := NewBuilder(nil)
	prompt, err := b.Build(repoPath, rangeRef, 0, 0, "test", "")
	if err != nil {
		t.Fatalf("Build (range) failed: %v", err)
	}

	// Should contain context files section
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Range prompt should contain context files section")
	}

	if !strings.Contains(prompt, "Range context test") {
		t.Error("Range prompt should contain context file content")
	}

	// Should still have range-specific sections
	if !strings.Contains(prompt, "## Commit Range") {
		t.Error("Range prompt should contain commit range section")
	}
}

func TestBuildAddressPromptWithContextFiles(t *testing.T) {
	repoPath := t.TempDir()

	// Create a context file
	contextFile := filepath.Join(repoPath, "ADR.md")
	if err := os.WriteFile(contextFile, []byte("# ADR\n\nAddress context test."), 0644); err != nil {
		t.Fatalf("Failed to write context file: %v", err)
	}

	// Create .roborev.toml with context_files
	configContent := `context_files = ["ADR.md"]`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Create a mock review
	review := &storage.Review{
		JobID:  123,
		Agent:  "test",
		Output: "Found some issues to address.",
	}

	b := NewBuilder(nil)
	prompt, err := b.BuildAddressPrompt(repoPath, review, nil)
	if err != nil {
		t.Fatalf("BuildAddressPrompt failed: %v", err)
	}

	// Should contain context files section
	if !strings.Contains(prompt, "## Context Files") {
		t.Error("Address prompt should contain context files section")
	}

	if !strings.Contains(prompt, "Address context test") {
		t.Error("Address prompt should contain context file content")
	}

	// Should still have address-specific sections
	if !strings.Contains(prompt, "## Review Findings to Address") {
		t.Error("Address prompt should contain review findings section")
	}
}
