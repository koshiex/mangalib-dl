package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/koshiex/mangalib-dl/internal/downloader"
)

const (
	queueSize   = 64
	maxJobs     = 100
	maxWarnings = 200
)

// JobState is the lifecycle state of a download job.
type JobState string

const (
	StateQueued    JobState = "queued"
	StateRunning   JobState = "running"
	StateDone      JobState = "done"
	StateFailed    JobState = "failed"
	StateCancelled JobState = "cancelled"
)

func (s JobState) finished() bool {
	return s == StateDone || s == StateFailed || s == StateCancelled
}

var (
	errQueueFull   = errors.New("download queue is full")
	errJobNotFound = errors.New("job not found")
)

// Runner executes a download. *downloader.Downloader implements it.
type Runner interface {
	Run(ctx context.Context, req downloader.Request, obs downloader.Observer) ([]downloader.FileResult, error)
}

// FileInfo is a produced archive as shown to the UI.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Skipped bool   `json:"skipped"`
}

// JobSnapshot is an immutable copy of a job state.
type JobSnapshot struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	State          JobState   `json:"state"`
	Error          string     `json:"error,omitempty"`
	Dir            string     `json:"dir,omitempty"`
	TotalChapters  int        `json:"totalChapters"`
	DoneChapters   int        `json:"doneChapters"`
	CurrentChapter string     `json:"currentChapter,omitempty"`
	CurrentPages   int        `json:"currentPages"`
	DonePages      int        `json:"donePages"`
	Files          []FileInfo `json:"files"`
	Warnings       []string   `json:"warnings"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// job is a download job. It implements downloader.Observer.
type job struct {
	mu     sync.Mutex
	snap   JobSnapshot
	paths  []string
	req    downloader.Request
	ctx    context.Context
	cancel context.CancelFunc
}

func (j *job) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.snap
	s.Files = append([]FileInfo{}, j.snap.Files...)
	s.Warnings = append([]string{}, j.snap.Warnings...)
	return s
}

func (j *job) update(fn func(s *JobSnapshot)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.snap)
}

func (j *job) Planned(p downloader.Plan) {
	j.update(func(s *JobSnapshot) {
		s.Title, s.Dir, s.TotalChapters = p.Title, p.Dir, p.Chapters
	})
}

func (j *job) ChapterStarted(label string, pages int) {
	j.update(func(s *JobSnapshot) {
		s.CurrentChapter, s.CurrentPages, s.DonePages = label, pages, 0
	})
}

func (j *job) PageDone() {
	j.update(func(s *JobSnapshot) { s.DonePages++ })
}

func (j *job) ChapterDone() {
	j.update(func(s *JobSnapshot) { s.DoneChapters++ })
}

func (j *job) FileDone(f downloader.FileResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.snap.Files = append(j.snap.Files, FileInfo{Name: f.Name, Size: f.Size, Skipped: f.Skipped})
	j.paths = append(j.paths, f.Path)
}

func (j *job) Warn(msg string) {
	j.update(func(s *JobSnapshot) {
		if len(s.Warnings) < maxWarnings {
			s.Warnings = append(s.Warnings, msg)
		}
	})
}

func (j *job) path(n int) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n < 0 || n >= len(j.paths) {
		return "", false
	}
	return j.paths[n], true
}

func (j *job) finish(err error) {
	j.update(func(s *JobSnapshot) {
		s.CurrentChapter = ""
		switch {
		case err == nil:
			s.State = StateDone
		case errors.Is(err, context.Canceled):
			s.State = StateCancelled
		default:
			s.State, s.Error = StateFailed, err.Error()
		}
	})
}

// JobManager runs download jobs one at a time so the API rate limit is shared fairly.
type JobManager struct {
	runner Runner
	ctx    context.Context
	queue  chan *job

	mu    sync.Mutex
	jobs  map[string]*job
	order []string
}

// NewJobManager starts a worker that runs jobs until ctx is done.
func NewJobManager(ctx context.Context, runner Runner) *JobManager {
	m := &JobManager{
		runner: runner,
		ctx:    ctx,
		queue:  make(chan *job, queueSize),
		jobs:   make(map[string]*job),
	}
	go m.loop()
	return m
}

func (m *JobManager) loop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case j := <-m.queue:
			m.run(j)
		}
	}
}

func (m *JobManager) run(j *job) {
	if j.ctx.Err() != nil {
		j.finish(j.ctx.Err())
		return
	}
	j.update(func(s *JobSnapshot) { s.State = StateRunning })
	_, err := m.runner.Run(j.ctx, j.req, j)
	j.finish(err)
}

// Submit queues a download.
func (m *JobManager) Submit(req downloader.Request) (JobSnapshot, error) {
	ctx, cancel := context.WithCancel(m.ctx)
	j := &job{
		req:    req,
		ctx:    ctx,
		cancel: cancel,
		snap:   JobSnapshot{ID: newID(), Slug: req.Slug, Title: req.Slug, State: StateQueued, CreatedAt: time.Now()},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case m.queue <- j:
	default:
		cancel()
		return JobSnapshot{}, errQueueFull
	}
	m.jobs[j.snap.ID] = j
	m.order = append(m.order, j.snap.ID)
	m.pruneLocked()
	return j.snapshot(), nil
}

// pruneLocked forgets the oldest finished jobs beyond maxJobs. Files on disk are kept.
func (m *JobManager) pruneLocked() {
	for i := 0; len(m.order) > maxJobs && i < len(m.order); {
		id := m.order[i]
		if !m.jobs[id].snapshot().State.finished() {
			i++
			continue
		}
		delete(m.jobs, id)
		m.order = append(m.order[:i:i], m.order[i+1:]...)
	}
}

// Cancel stops a queued or running job.
func (m *JobManager) Cancel(id string) error {
	j, ok := m.lookup(id)
	if !ok {
		return errJobNotFound
	}
	j.cancel()
	j.update(func(s *JobSnapshot) {
		if s.State == StateQueued {
			s.State = StateCancelled
		}
	})
	return nil
}

// Get returns a job snapshot.
func (m *JobManager) Get(id string) (JobSnapshot, bool) {
	j, ok := m.lookup(id)
	if !ok {
		return JobSnapshot{}, false
	}
	return j.snapshot(), true
}

// List returns all jobs, newest first.
func (m *JobManager) List() []JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]JobSnapshot, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.jobs[m.order[i]].snapshot())
	}
	return out
}

// FilePath returns the path of the n-th archive produced by a job.
func (m *JobManager) FilePath(id string, n int) (string, bool) {
	j, ok := m.lookup(id)
	if !ok {
		return "", false
	}
	return j.path(n)
}

func (m *JobManager) lookup(id string) (*job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}
