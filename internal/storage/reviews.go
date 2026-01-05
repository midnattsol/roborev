package storage

import (
	"database/sql"
	"time"
)

// GetReviewByJobID finds a review by its job ID
func (db *DB) GetReviewByJobID(jobID int64) (*Review, error) {
	var r Review
	var createdAt string
	var job ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg sql.NullString

	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at,
		       j.id, j.repo_id, j.commit_id, j.agent, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       rp.root_path, rp.name, c.sha, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		JOIN commits c ON c.id = j.commit_id
		WHERE rv.job_id = ?
	`, jobID).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt,
		&job.ID, &job.RepoID, &job.CommitID, &job.Agent, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg,
		&job.RepoPath, &job.RepoName, &job.CommitSHA, &job.CommitSubject)
	if err != nil {
		return nil, err
	}

	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	job.EnqueuedAt, _ = time.Parse(time.RFC3339, enqueuedAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		job.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse(time.RFC3339, finishedAt.String)
		job.FinishedAt = &t
	}
	if workerID.Valid {
		job.WorkerID = workerID.String
	}
	if errMsg.Valid {
		job.Error = errMsg.String
	}
	r.Job = &job

	return &r, nil
}

// GetReviewByCommitSHA finds the most recent review by commit SHA
func (db *DB) GetReviewByCommitSHA(sha string) (*Review, error) {
	var r Review
	var createdAt string
	var job ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg sql.NullString

	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at,
		       j.id, j.repo_id, j.commit_id, j.agent, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       rp.root_path, rp.name, c.sha, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		JOIN commits c ON c.id = j.commit_id
		WHERE c.sha = ?
		ORDER BY rv.created_at DESC
		LIMIT 1
	`, sha).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt,
		&job.ID, &job.RepoID, &job.CommitID, &job.Agent, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg,
		&job.RepoPath, &job.RepoName, &job.CommitSHA, &job.CommitSubject)
	if err != nil {
		return nil, err
	}

	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	job.EnqueuedAt, _ = time.Parse(time.RFC3339, enqueuedAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		job.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse(time.RFC3339, finishedAt.String)
		job.FinishedAt = &t
	}
	if workerID.Valid {
		job.WorkerID = workerID.String
	}
	if errMsg.Valid {
		job.Error = errMsg.String
	}
	r.Job = &job

	return &r, nil
}

// GetRecentReviewsForRepo returns the N most recent reviews for a repo
func (db *DB) GetRecentReviewsForRepo(repoID int64, limit int) ([]Review, error) {
	rows, err := db.Query(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		WHERE j.repo_id = ?
		ORDER BY rv.created_at DESC
		LIMIT ?
	`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var r Review
		var createdAt string
		if err := rows.Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		reviews = append(reviews, r)
	}

	return reviews, rows.Err()
}

// AddResponse adds a response to a commit
func (db *DB) AddResponse(commitID int64, responder, response string) (*Response, error) {
	result, err := db.Exec(`INSERT INTO responses (commit_id, responder, response) VALUES (?, ?, ?)`,
		commitID, responder, response)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &Response{
		ID:        id,
		CommitID:  commitID,
		Responder: responder,
		Response:  response,
		CreatedAt: time.Now(),
	}, nil
}

// GetResponsesForCommit returns all responses for a commit
func (db *DB) GetResponsesForCommit(commitID int64) ([]Response, error) {
	rows, err := db.Query(`
		SELECT id, commit_id, responder, response, created_at
		FROM responses
		WHERE commit_id = ?
		ORDER BY created_at ASC
	`, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []Response
	for rows.Next() {
		var r Response
		var createdAt string
		if err := rows.Scan(&r.ID, &r.CommitID, &r.Responder, &r.Response, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		responses = append(responses, r)
	}

	return responses, rows.Err()
}

// GetResponsesForCommitSHA returns all responses for a commit by SHA
func (db *DB) GetResponsesForCommitSHA(sha string) ([]Response, error) {
	commit, err := db.GetCommitBySHA(sha)
	if err != nil {
		return nil, err
	}
	return db.GetResponsesForCommit(commit.ID)
}
