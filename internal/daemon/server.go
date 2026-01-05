package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/user/roborev/internal/config"
	"github.com/user/roborev/internal/git"
	"github.com/user/roborev/internal/storage"
)

// Server is the HTTP API server for the daemon
type Server struct {
	db         *storage.DB
	cfg        *config.Config
	workerPool *WorkerPool
	httpServer *http.Server
}

// NewServer creates a new daemon server
func NewServer(db *storage.DB, cfg *config.Config) *Server {
	s := &Server{
		db:         db,
		cfg:        cfg,
		workerPool: NewWorkerPool(db, cfg, cfg.MaxWorkers),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", s.handleEnqueue)
	mux.HandleFunc("/api/jobs", s.handleListJobs)
	mux.HandleFunc("/api/review", s.handleGetReview)
	mux.HandleFunc("/api/respond", s.handleAddResponse)
	mux.HandleFunc("/api/responses", s.handleListResponses)
	mux.HandleFunc("/api/status", s.handleStatus)

	s.httpServer = &http.Server{
		Addr:    cfg.ServerAddr,
		Handler: mux,
	}

	return s
}

// Start begins the server and worker pool
func (s *Server) Start() error {
	// Reset stale jobs from previous runs
	if err := s.db.ResetStaleJobs(); err != nil {
		log.Printf("Warning: failed to reset stale jobs: %v", err)
	}

	// Find available port
	addr, port, err := FindAvailablePort(s.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("find available port: %w", err)
	}
	s.httpServer.Addr = addr

	// Write runtime info so CLI can find us
	if err := WriteRuntime(addr, port); err != nil {
		log.Printf("Warning: failed to write runtime info: %v", err)
	}

	// Start worker pool
	s.workerPool.Start()

	// Start HTTP server
	log.Printf("Starting HTTP server on %s", addr)
	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Remove runtime info
	RemoveRuntime()

	// Stop HTTP server
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Stop worker pool
	s.workerPool.Stop()

	return nil
}

// API request/response types

type EnqueueRequest struct {
	RepoPath  string `json:"repo_path"`
	CommitSHA string `json:"commit_sha"`
	Agent     string `json:"agent,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func (s *Server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RepoPath == "" || req.CommitSHA == "" {
		writeError(w, http.StatusBadRequest, "repo_path and commit_sha are required")
		return
	}

	// Resolve repo root and SHA
	repoRoot, err := git.GetRepoRoot(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("not a git repository: %v", err))
		return
	}

	sha, err := git.ResolveSHA(repoRoot, req.CommitSHA)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid commit: %v", err))
		return
	}

	// Get commit info
	info, err := git.GetCommitInfo(repoRoot, sha)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("get commit info: %v", err))
		return
	}

	// Get or create repo
	repo, err := s.db.GetOrCreateRepo(repoRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get repo: %v", err))
		return
	}

	// Get or create commit
	commit, err := s.db.GetOrCreateCommit(repo.ID, sha, info.Author, info.Subject, info.Timestamp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get commit: %v", err))
		return
	}

	// Resolve agent
	agent := config.ResolveAgent(req.Agent, repoRoot, s.cfg)

	// Create job
	job, err := s.db.EnqueueJob(repo.ID, commit.ID, agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue job: %v", err))
		return
	}

	// Fill in joined fields
	job.RepoPath = repo.RootPath
	job.RepoName = repo.Name
	job.CommitSHA = commit.SHA
	job.CommitSubject = commit.Subject

	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status := r.URL.Query().Get("status")
	limit := 50 // default

	jobs, err := s.db.ListJobs(status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list jobs: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": jobs})
}

func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var review *storage.Review
	var err error

	// Support lookup by job_id (preferred) or sha
	if jobIDStr := r.URL.Query().Get("job_id"); jobIDStr != "" {
		var jobID int64
		if _, err := fmt.Sscanf(jobIDStr, "%d", &jobID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid job_id")
			return
		}
		review, err = s.db.GetReviewByJobID(jobID)
	} else if sha := r.URL.Query().Get("sha"); sha != "" {
		review, err = s.db.GetReviewByCommitSHA(sha)
	} else {
		writeError(w, http.StatusBadRequest, "job_id or sha parameter required")
		return
	}

	if err != nil {
		writeError(w, http.StatusNotFound, "review not found")
		return
	}

	writeJSON(w, http.StatusOK, review)
}

type AddResponseRequest struct {
	SHA       string `json:"sha"`
	Responder string `json:"responder"`
	Response  string `json:"response"`
}

func (s *Server) handleAddResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req AddResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.SHA == "" || req.Responder == "" || req.Response == "" {
		writeError(w, http.StatusBadRequest, "sha, responder, and response are required")
		return
	}

	commit, err := s.db.GetCommitBySHA(req.SHA)
	if err != nil {
		writeError(w, http.StatusNotFound, "commit not found")
		return
	}

	resp, err := s.db.AddResponse(commit.ID, req.Responder, req.Response)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("add response: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleListResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	sha := r.URL.Query().Get("sha")
	if sha == "" {
		writeError(w, http.StatusBadRequest, "sha parameter required")
		return
	}

	responses, err := s.db.GetResponsesForCommitSHA(sha)
	if err != nil {
		writeError(w, http.StatusNotFound, "commit not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"responses": responses})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	queued, running, done, failed, err := s.db.GetJobCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get counts: %v", err))
		return
	}

	status := storage.DaemonStatus{
		QueuedJobs:    queued,
		RunningJobs:   running,
		CompletedJobs: done,
		FailedJobs:    failed,
		ActiveWorkers: s.workerPool.ActiveWorkers(),
		MaxWorkers:    s.cfg.MaxWorkers,
	}

	writeJSON(w, http.StatusOK, status)
}
