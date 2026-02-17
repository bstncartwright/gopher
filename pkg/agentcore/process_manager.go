package agentcore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type ProcessSession struct {
	ID        string
	Command   string
	PID       int
	StartedAt time.Time

	ExitCode  *int
	Done      bool
	Output    []string
	PollIndex int

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
}

type ProcessManager struct {
	mu       sync.Mutex
	sessions map[string]*ProcessSession
	counter  uint64
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		sessions: make(map[string]*ProcessSession),
	}
}

func (pm *ProcessManager) Start(ctx context.Context, command string, workdir string, env map[string]string, timeout time.Duration) (*ProcessSession, error) {
	id := fmt.Sprintf("bg-%d-%d", atomic.AddUint64(&pm.counter, 1), time.Now().UnixMilli())

	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	if len(env) > 0 {
		cmd.Env = cmd.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	session := &ProcessSession{
		ID:        id,
		Command:   command,
		StartedAt: time.Now(),
		cmd:       cmd,
		stdin:     stdinPipe,
		cancel:    cancel,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		pw.Close()
		pr.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	session.PID = cmd.Process.Pid

	pm.mu.Lock()
	pm.sessions[id] = session
	pm.mu.Unlock()

	// Line collector: reads combined stdout+stderr line by line.
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			pm.mu.Lock()
			session.Output = append(session.Output, scanner.Text())
			pm.mu.Unlock()
		}
	}()

	// Waiter: sets exit code and done flag once the process finishes.
	go func() {
		waitErr := cmd.Wait()
		pw.Close()

		code := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}

		pm.mu.Lock()
		session.ExitCode = &code
		session.Done = true
		pm.mu.Unlock()
	}()

	return session, nil
}

func (pm *ProcessManager) Get(sessionID string) (*ProcessSession, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	s, ok := pm.sessions[sessionID]
	return s, ok
}

func (pm *ProcessManager) List() []*ProcessSession {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]*ProcessSession, 0, len(pm.sessions))
	for _, s := range pm.sessions {
		out = append(out, s)
	}
	return out
}

func (pm *ProcessManager) Poll(sessionID string) (newOutput []string, exitCode *int, done bool, err error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	s, ok := pm.sessions[sessionID]
	if !ok {
		return nil, nil, false, fmt.Errorf("session %q not found", sessionID)
	}

	newOutput = make([]string, len(s.Output[s.PollIndex:]))
	copy(newOutput, s.Output[s.PollIndex:])
	s.PollIndex = len(s.Output)

	return newOutput, s.ExitCode, s.Done, nil
}

func (pm *ProcessManager) Log(sessionID string, offset int, limit int) (lines []string, total int, err error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	s, ok := pm.sessions[sessionID]
	if !ok {
		return nil, 0, fmt.Errorf("session %q not found", sessionID)
	}

	total = len(s.Output)
	if limit <= 0 {
		limit = 200
	}

	var start, end int
	if offset < 0 {
		// Negative or zero offset: return last `limit` lines.
		start = total - limit
		if start < 0 {
			start = 0
		}
		end = total
	} else {
		start = offset
		if start > total {
			start = total
		}
		end = start + limit
		if end > total {
			end = total
		}
	}

	out := make([]string, end-start)
	copy(out, s.Output[start:end])
	return out, total, nil
}

func (pm *ProcessManager) Write(sessionID string, data string) error {
	pm.mu.Lock()
	s, ok := pm.sessions[sessionID]
	if !ok {
		pm.mu.Unlock()
		return fmt.Errorf("session %q not found", sessionID)
	}
	if s.Done {
		pm.mu.Unlock()
		return fmt.Errorf("session %q is already done", sessionID)
	}
	stdin := s.stdin
	pm.mu.Unlock()

	_, err := io.WriteString(stdin, data)
	if err != nil {
		return fmt.Errorf("write to session %q stdin: %w", sessionID, err)
	}
	return nil
}

func (pm *ProcessManager) Kill(sessionID string) error {
	pm.mu.Lock()
	s, ok := pm.sessions[sessionID]
	if !ok {
		pm.mu.Unlock()
		return fmt.Errorf("session %q not found", sessionID)
	}
	if s.Done {
		pm.mu.Unlock()
		return fmt.Errorf("session %q is already done", sessionID)
	}
	cancel := s.cancel
	stdin := s.stdin
	pm.mu.Unlock()

	stdin.Close()
	cancel()
	return nil
}

func (pm *ProcessManager) Cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for id, s := range pm.sessions {
		if s.Done && s.StartedAt.Before(cutoff) {
			delete(pm.sessions, id)
		}
	}
}
