package storage

import (
	"database/sql"
	"time"
)

// EnqueueJob creates a new review job for a single commit
func (db *DB) EnqueueJob(repoID, commitID int64, gitRef, agent string) (*ReviewJob, error) {
	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, ?, ?, ?, 'queued')`,
		repoID, commitID, gitRef, agent)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:         id,
		RepoID:     repoID,
		CommitID:   &commitID,
		GitRef:     gitRef,
		Agent:      agent,
		Status:     JobStatusQueued,
		EnqueuedAt: time.Now(),
	}, nil
}

// EnqueueRangeJob creates a new review job for a commit range
func (db *DB) EnqueueRangeJob(repoID int64, gitRef, agent string) (*ReviewJob, error) {
	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, NULL, ?, ?, 'queued')`,
		repoID, gitRef, agent)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:         id,
		RepoID:     repoID,
		CommitID:   nil,
		GitRef:     gitRef,
		Agent:      agent,
		Status:     JobStatusQueued,
		EnqueuedAt: time.Now(),
	}, nil
}

// ClaimJob atomically claims the next queued job for a worker
func (db *DB) ClaimJob(workerID string) (*ReviewJob, error) {
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	// Atomically claim a job by updating it in a single statement
	// This prevents race conditions where two workers select the same job
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'running', worker_id = ?, started_at = ?
		WHERE id = (
			SELECT id FROM review_jobs
			WHERE status = 'queued'
			ORDER BY enqueued_at
			LIMIT 1
		)
	`, workerID, nowStr)
	if err != nil {
		return nil, err
	}

	// Check if we claimed anything
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected == 0 {
		return nil, nil // No jobs available
	}

	// Now fetch the job we just claimed
	var job ReviewJob
	var enqueuedAt string
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	err = db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.status, j.enqueued_at,
		       r.root_path, r.name, c.subject
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.worker_id = ? AND j.status = 'running'
		ORDER BY j.started_at DESC
		LIMIT 1
	`, workerID).Scan(&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Status, &enqueuedAt,
		&job.RepoPath, &job.RepoName, &commitSubject)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		job.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		job.CommitSubject = commitSubject.String
	}
	job.EnqueuedAt, _ = time.Parse(time.RFC3339, enqueuedAt)
	job.Status = JobStatusRunning
	job.WorkerID = workerID
	job.StartedAt = &now
	return &job, nil
}

// CompleteJob marks a job as done and stores the review
func (db *DB) CompleteJob(jobID int64, agent, prompt, output string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)

	// Update job status
	_, err = tx.Exec(`UPDATE review_jobs SET status = 'done', finished_at = ? WHERE id = ?`, now, jobID)
	if err != nil {
		return err
	}

	// Insert review
	_, err = tx.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, ?, ?)`,
		jobID, agent, prompt, output)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// FailJob marks a job as failed with an error message
func (db *DB) FailJob(jobID int64, errorMsg string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ? WHERE id = ?`,
		now, errorMsg, jobID)
	return err
}

// ListJobs returns jobs with optional status filter
func (db *DB) ListJobs(statusFilter string, limit int) ([]ReviewJob, error) {
	query := `
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       r.root_path, r.name, c.subject
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
	`
	var args []interface{}

	if statusFilter != "" {
		query += " WHERE j.status = ?"
		args = append(args, statusFilter)
	}

	query += " ORDER BY j.enqueued_at DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []ReviewJob
	for rows.Next() {
		var j ReviewJob
		var enqueuedAt string
		var startedAt, finishedAt, workerID, errMsg sql.NullString
		var commitID sql.NullInt64
		var commitSubject sql.NullString

		err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &j.Agent, &j.Status, &enqueuedAt,
			&startedAt, &finishedAt, &workerID, &errMsg,
			&j.RepoPath, &j.RepoName, &commitSubject)
		if err != nil {
			return nil, err
		}

		if commitID.Valid {
			j.CommitID = &commitID.Int64
		}
		if commitSubject.Valid {
			j.CommitSubject = commitSubject.String
		}
		j.EnqueuedAt, _ = time.Parse(time.RFC3339, enqueuedAt)
		if startedAt.Valid {
			t, _ := time.Parse(time.RFC3339, startedAt.String)
			j.StartedAt = &t
		}
		if finishedAt.Valid {
			t, _ := time.Parse(time.RFC3339, finishedAt.String)
			j.FinishedAt = &t
		}
		if workerID.Valid {
			j.WorkerID = workerID.String
		}
		if errMsg.Valid {
			j.Error = errMsg.String
		}

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

// GetJobByID returns a job by ID with joined fields
func (db *DB) GetJobByID(id int64) (*ReviewJob, error) {
	var j ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString

	err := db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       r.root_path, r.name, c.subject
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.id = ?
	`, id).Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &j.Agent, &j.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg,
		&j.RepoPath, &j.RepoName, &commitSubject)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		j.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		j.CommitSubject = commitSubject.String
	}
	j.EnqueuedAt, _ = time.Parse(time.RFC3339, enqueuedAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		j.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse(time.RFC3339, finishedAt.String)
		j.FinishedAt = &t
	}
	if workerID.Valid {
		j.WorkerID = workerID.String
	}
	if errMsg.Valid {
		j.Error = errMsg.String
	}

	return &j, nil
}

// GetJobCounts returns counts of jobs by status
func (db *DB) GetJobCounts() (queued, running, done, failed int, err error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM review_jobs GROUP BY status`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			return
		}
		switch JobStatus(status) {
		case JobStatusQueued:
			queued = count
		case JobStatusRunning:
			running = count
		case JobStatusDone:
			done = count
		case JobStatusFailed:
			failed = count
		}
	}
	err = rows.Err()
	return
}
