package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID           string                 `json:"id"`
	AgentID      string                 `json:"agent_id"`
	Workspace    string                 `json:"workspace"`
	Type         string                 `json:"type"`
	Params       map[string]interface{} `json:"params"`
	ScheduledAt  time.Time              `json:"scheduled_at"`
	CreatedAt    time.Time              `json:"created_at"`
	Status       string                 `json:"status"`
	LastRun      *time.Time             `json:"last_run,omitempty"`
	NextRun      *time.Time             `json:"next_run,omitempty"`
	Result       *JobResult             `json:"result,omitempty"`
}

type JobResult struct {
	Success   bool                   `json:"success"`
	Output    string                 `json:"output,omitempty"`
	Error     string                 `json:"error,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	FinishedAt time.Time             `json:"finished_at"`
}

type Store struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewStore() *Store {
	return &Store{
		jobs: make(map[string]*Job),
	}
}

func (s *Store) Add(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	job.Status = "pending"

	s.jobs[job.ID] = job
	return nil
}

func (s *Store) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	return job, ok
}

func (s *Store) ListByAgent(agentID string) []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Job
	for _, job := range s.jobs {
		if job.AgentID == agentID {
			result = append(result, job)
		}
	}
	return result
}

func (s *Store) ListByWorkspace(workspace string) []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Job
	for _, job := range s.jobs {
		if job.Workspace == workspace {
			result = append(result, job)
		}
	}
	return result
}

func (s *Store) Update(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[job.ID]; !ok {
		return fmt.Errorf("job not found: %s", job.ID)
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	delete(s.jobs, id)
	return nil
}

func (s *Store) Cancel(id, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	if job.AgentID != agentID {
		return fmt.Errorf("unauthorized")
	}
	job.Status = "cancelled"
	return nil
}

type Scheduler struct {
	store    *Store
	mu      sync.RWMutex
	running bool
	stopCh  chan struct{}
}

func NewScheduler(store *Store) *Scheduler {
	return &Scheduler{
		store: store,
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler already running")
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	go s.run(ctx)
	return nil
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		close(s.stopCh)
		s.running = false
	}
}

func (s *Scheduler) run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.processJobs()
		}
	}
}

func (s *Scheduler) processJobs() {
	s.store.mu.RLock()
	var pending []*Job
	for _, job := range s.store.jobs {
		if job.Status == "pending" && job.ScheduledAt.Before(time.Now()) {
			pending = append(pending, job)
		}
	}
	s.store.mu.RUnlock()

	for _, job := range pending {
		s.executeJob(job)
	}
}

func (s *Scheduler) executeJob(job *Job) {
	now := time.Now()
	job.Status = "running"
	job.LastRun = &now
	s.store.Update(job)

	result := &JobResult{
		StartedAt: now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch job.Type {
	case "clockify.wrapup":
		err := s.executeClockifyWrapup(ctx, job)
		result.Success = err == nil
		if err != nil {
			result.Error = err.Error()
		}
	default:
		result.Success = false
		result.Error = fmt.Sprintf("unknown job type: %s", job.Type)
	}

	result.FinishedAt = time.Now()
	job.Result = result
	if result.Success {
		job.Status = "completed"
	} else {
		job.Status = "failed"
	}
	s.store.Update(job)
}

func (s *Scheduler) executeClockifyWrapup(ctx context.Context, job *Job) error {
	return fmt.Errorf("clockify scheduler not yet connected to gateway — requires POST /admin/jobs endpoint and connector wiring; see roadmap M5")
}

type WrapupParams struct {
	JiraKey         string `json:"jira_key"`
	EndTime         string `json:"end_time"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	OnEndStatus     string `json:"on_end_status"`
	ClockifyProject string `json:"clockify_project"`
}

func ParseWrapupParams(data []byte) (*WrapupParams, error) {
	var params WrapupParams
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, err
	}
	return &params, nil
}
