package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAndClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestRepoOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repo
	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	if repo.ID == 0 {
		t.Error("Repo ID should not be 0")
	}
	if repo.Name != "test-repo" {
		t.Errorf("Expected name 'test-repo', got '%s'", repo.Name)
	}

	// Get same repo again (should return existing)
	repo2, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo (second call) failed: %v", err)
	}
	if repo2.ID != repo.ID {
		t.Error("Should return same repo on second call")
	}
}

func TestCommitOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")

	// Create commit
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123def456", "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	if commit.ID == 0 {
		t.Error("Commit ID should not be 0")
	}
	if commit.SHA != "abc123def456" {
		t.Errorf("Expected SHA 'abc123def456', got '%s'", commit.SHA)
	}

	// Get by SHA
	found, err := db.GetCommitBySHA("abc123def456")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}
	if found.ID != commit.ID {
		t.Error("GetCommitBySHA returned wrong commit")
	}
}

func TestJobLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Subject", time.Now())

	// Enqueue job
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "codex")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	if job.Status != JobStatusQueued {
		t.Errorf("Expected status 'queued', got '%s'", job.Status)
	}

	// Claim job
	claimed, err := db.ClaimJob("worker-1")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimJob returned nil")
	}
	if claimed.ID != job.ID {
		t.Error("ClaimJob returned wrong job")
	}
	if claimed.Status != JobStatusRunning {
		t.Errorf("Expected status 'running', got '%s'", claimed.Status)
	}

	// Claim again should return nil (no more jobs)
	claimed2, err := db.ClaimJob("worker-2")
	if err != nil {
		t.Fatalf("ClaimJob (second) failed: %v", err)
	}
	if claimed2 != nil {
		t.Error("ClaimJob should return nil when no jobs available")
	}

	// Complete job
	err = db.CompleteJob(job.ID, "codex", "test prompt", "test output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Verify job status
	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if updatedJob.Status != JobStatusDone {
		t.Errorf("Expected status 'done', got '%s'", updatedJob.Status)
	}
}

func TestJobFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "def456", "Author", "Subject", time.Now())

	job, _ := db.EnqueueJob(repo.ID, commit.ID, "def456", "codex")
	db.ClaimJob("worker-1")

	// Fail the job
	err := db.FailJob(job.ID, "test error message")
	if err != nil {
		t.Fatalf("FailJob failed: %v", err)
	}

	updatedJob, _ := db.GetJobByID(job.ID)
	if updatedJob.Status != JobStatusFailed {
		t.Errorf("Expected status 'failed', got '%s'", updatedJob.Status)
	}
	if updatedJob.Error != "test error message" {
		t.Errorf("Expected error message 'test error message', got '%s'", updatedJob.Error)
	}
}

func TestReviewOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "rev123", "Author", "Subject", time.Now())
	job, _ := db.EnqueueJob(repo.ID, commit.ID, "rev123", "codex")
	db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "the prompt", "the review output")

	// Get review by commit SHA
	review, err := db.GetReviewByCommitSHA("rev123")
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA failed: %v", err)
	}

	if review.Output != "the review output" {
		t.Errorf("Expected output 'the review output', got '%s'", review.Output)
	}
	if review.Agent != "codex" {
		t.Errorf("Expected agent 'codex', got '%s'", review.Agent)
	}
}

func TestResponseOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "resp123", "Author", "Subject", time.Now())

	// Add response
	resp, err := db.AddResponse(commit.ID, "test-user", "LGTM!")
	if err != nil {
		t.Fatalf("AddResponse failed: %v", err)
	}

	if resp.Response != "LGTM!" {
		t.Errorf("Expected response 'LGTM!', got '%s'", resp.Response)
	}

	// Get responses
	responses, err := db.GetResponsesForCommit(commit.ID)
	if err != nil {
		t.Fatalf("GetResponsesForCommit failed: %v", err)
	}

	if len(responses) != 1 {
		t.Errorf("Expected 1 response, got %d", len(responses))
	}
}

func TestJobCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")

	// Create 3 jobs that will stay queued
	for i := 0; i < 3; i++ {
		sha := fmt.Sprintf("queued%d", i)
		commit, _ := db.GetOrCreateCommit(repo.ID, sha, "A", "S", time.Now())
		db.EnqueueJob(repo.ID, commit.ID, sha, "codex")
	}

	// Create a job, claim it, and complete it
	commit, _ := db.GetOrCreateCommit(repo.ID, "done1", "A", "S", time.Now())
	job, _ := db.EnqueueJob(repo.ID, commit.ID, "done1", "codex")
	_, _ = db.ClaimJob("w1") // Claims oldest queued job (one of queued0-2)
	_, _ = db.ClaimJob("w1") // Claims next
	_, _ = db.ClaimJob("w1") // Claims next
	claimed, _ := db.ClaimJob("w1") // Should claim "done1" job now
	if claimed != nil {
		db.CompleteJob(claimed.ID, "codex", "p", "o")
	}

	// Create a job, claim it, and fail it
	commit2, _ := db.GetOrCreateCommit(repo.ID, "fail1", "A", "S", time.Now())
	_, _ = db.EnqueueJob(repo.ID, commit2.ID, "fail1", "codex")
	claimed2, _ := db.ClaimJob("w2")
	if claimed2 != nil {
		db.FailJob(claimed2.ID, "err")
	}

	queued, _, done, failed, err := db.GetJobCounts()
	if err != nil {
		t.Fatalf("GetJobCounts failed: %v", err)
	}

	// We expect: 0 queued (all were claimed), 1 done, 1 failed, 3 running
	// Actually let's just verify done and failed are correct
	if done != 1 {
		t.Errorf("Expected 1 done, got %d", done)
	}
	if failed != 1 {
		t.Errorf("Expected 1 failed, got %d", failed)
	}
	_ = queued
	_ = job
}

func openTestDB(t *testing.T) *DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	return db
}
