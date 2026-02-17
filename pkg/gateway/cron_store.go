package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type CronStore interface {
	List() []CronJob
	Get(jobID string) (CronJob, bool)
	Create(job CronJob) error
	Update(job CronJob) error
	Delete(jobID string) bool
}

type FileCronStore struct {
	mu       sync.RWMutex
	filePath string
	jobs     map[string]CronJob
}

type cronStoreDisk struct {
	Jobs []CronJob `json:"jobs"`
}

func NewFileCronStore(filePath string) (*FileCronStore, error) {
	path := strings.TrimSpace(filePath)
	if path == "" {
		return nil, fmt.Errorf("cron store file path is required")
	}
	store := &FileCronStore{
		filePath: path,
		jobs:     map[string]CronJob{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileCronStore) List() []CronJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CronJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, cloneCronJob(job))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *FileCronStore) Get(jobID string) (CronJob, bool) {
	key := strings.TrimSpace(jobID)
	if key == "" {
		return CronJob{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[key]
	if !ok {
		return CronJob{}, false
	}
	return cloneCronJob(job), true
}

func (s *FileCronStore) Create(job CronJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("cron job already exists: %s", job.ID)
	}
	s.jobs[job.ID] = cloneCronJob(job)
	return s.persistLocked()
}

func (s *FileCronStore) Update(job CronJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; !exists {
		return fmt.Errorf("cron job not found: %s", job.ID)
	}
	s.jobs[job.ID] = cloneCronJob(job)
	return s.persistLocked()
}

func (s *FileCronStore) Delete(jobID string) bool {
	key := strings.TrimSpace(jobID)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[key]; !ok {
		return false
	}
	removed := s.jobs[key]
	delete(s.jobs, key)
	if err := s.persistLocked(); err != nil {
		// Roll back in-memory state so callers never observe a delete that
		// wasn't durably persisted.
		s.jobs[key] = removed
		return false
	}
	return true
}

func (s *FileCronStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cron store: %w", err)
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil
	}
	var disk cronStoreDisk
	if err := json.Unmarshal(blob, &disk); err != nil {
		return fmt.Errorf("decode cron store: %w", err)
	}
	for _, job := range disk.Jobs {
		if strings.TrimSpace(job.ID) == "" {
			continue
		}
		s.jobs[job.ID] = cloneCronJob(job)
	}
	return nil
}

func (s *FileCronStore) persistLocked() error {
	jobs := make([]CronJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, cloneCronJob(job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].ID < jobs[j].ID
	})
	blob, err := json.Marshal(cronStoreDisk{Jobs: jobs})
	if err != nil {
		return fmt.Errorf("encode cron store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return fmt.Errorf("create cron store directory: %w", err)
	}
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0o644); err != nil {
		return fmt.Errorf("write cron store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		return fmt.Errorf("replace cron store file: %w", err)
	}
	return nil
}

func cloneCronJob(in CronJob) CronJob {
	out := in
	if in.LastRunAt != nil {
		timestamp := in.LastRunAt.UTC()
		out.LastRunAt = &timestamp
	}
	if in.NextRunAt != nil {
		timestamp := in.NextRunAt.UTC()
		out.NextRunAt = &timestamp
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out
}

type InMemoryCronStore struct {
	mu   sync.RWMutex
	jobs map[string]CronJob
}

func NewInMemoryCronStore() *InMemoryCronStore {
	return &InMemoryCronStore{jobs: map[string]CronJob{}}
}

func (s *InMemoryCronStore) List() []CronJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CronJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, cloneCronJob(job))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *InMemoryCronStore) Get(jobID string) (CronJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[strings.TrimSpace(jobID)]
	if !ok {
		return CronJob{}, false
	}
	return cloneCronJob(job), true
}

func (s *InMemoryCronStore) Create(job CronJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("cron job already exists: %s", job.ID)
	}
	s.jobs[job.ID] = cloneCronJob(job)
	return nil
}

func (s *InMemoryCronStore) Update(job CronJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; !exists {
		return fmt.Errorf("cron job not found: %s", job.ID)
	}
	s.jobs[job.ID] = cloneCronJob(job)
	return nil
}

func (s *InMemoryCronStore) Delete(jobID string) bool {
	key := strings.TrimSpace(jobID)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[key]; !ok {
		return false
	}
	delete(s.jobs, key)
	return true
}
