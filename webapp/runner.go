package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// matchaBinary is never derived from user input: the settings UI can change
// matcha's configuration but not what gets executed. The environment override
// exists so the binary can be exercised outside the image.
var matchaBinary = envOr("MATCHA_BINARY", "/usr/local/bin/matcha")

// Run sources, recorded so the UI can tell "the schedule fired" apart from
// "someone clicked the button" -- which is what usually answers "why is my
// digest empty?".
const (
	runSourceSchedule  = "schedule"
	runSourceManual    = "manual"
	runSourceManualCLI = "manual-cli"
)

const (
	// Keep the last chunk of output rather than the whole thing: a run with
	// reading_time enabled over a large feed list can be very chatty.
	maxRunLogBytes = 256 * 1024
	maxOutputTail  = 4000
	maxRunDuration = 30 * time.Minute
	killGraceDelay = 10 * time.Second
)

// ErrRunInProgress means another run holds the lock, in this process or in a
// separately launched `webapp -run`.
var ErrRunInProgress = errors.New("a matcha run is already in progress")

// RunResult is persisted to last-run.json after every run, whether it came
// from the scheduler, the UI, or the CLI.
type RunResult struct {
	Source      string    `json:"source"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationSec float64   `json:"duration_sec"`
	ExitCode    int       `json:"exit_code"`
	OK          bool      `json:"ok"`
	TimedOut    bool      `json:"timed_out"`
	OutputTail  string    `json:"output_tail"`
	Error       string    `json:"error,omitempty"`
}

// tailBuffer keeps only the trailing maxBytes of everything written to it.
type tailBuffer struct {
	mu       sync.Mutex
	maxBytes int
	buf      []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.maxBytes {
		t.buf = append(t.buf[:0:0], t.buf[len(t.buf)-t.maxBytes:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// tail returns at most n trailing bytes, trimmed to a rune boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < len(s) && i < 4; i++ {
		if s[i]&0xC0 != 0x80 {
			return s[i:]
		}
	}
	return s
}

// runner owns matcha execution and the last-run record.
type runner struct {
	settings *settingsStore

	mu        sync.Mutex
	running   bool
	startedAt time.Time
	last      *RunResult
}

func newRunner(st *settingsStore) *runner {
	r := &runner{settings: st}
	r.last = readLastRun()
	return r
}

func (r *runner) Status() (running bool, startedAt time.Time, last *RunResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running, r.startedAt, r.last
}

// acquire takes the in-process flag, then the cross-process lock. Both are
// needed: the flag keeps the UI honest, and the file lock stops a
// `docker exec ... matcha-runner` from racing the scheduler.
func (r *runner) acquire() (*os.File, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil, ErrRunInProgress
	}
	r.running = true
	r.startedAt = time.Now()
	r.mu.Unlock()

	lock, err := acquireRunLock()
	if err != nil {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
		return nil, err
	}
	return lock, nil
}

func (r *runner) release(lock *os.File, res *RunResult) {
	releaseRunLock(lock)
	r.mu.Lock()
	r.running = false
	if res != nil {
		r.last = res
	}
	r.mu.Unlock()
}

// Run regenerates config.yaml from the current settings and executes matcha.
//
// matcha is invoked directly rather than through matcha-runner, which now
// shells back into this binary and would recurse.
func (r *runner) Run(ctx context.Context, source string) (RunResult, error) {
	lock, err := r.acquire()
	if err != nil {
		return RunResult{}, err
	}

	started := time.Now()
	res := RunResult{Source: source, StartedAt: started}
	defer func() { r.release(lock, &res) }()

	settings := r.settings.Get()
	if err := WriteConfig(settings, generatedConfigPath()); err != nil {
		res.FinishedAt = time.Now()
		res.DurationSec = res.FinishedAt.Sub(started).Seconds()
		res.ExitCode = -1
		res.Error = "could not generate config.yaml: " + err.Error()
		writeLastRun(res)
		return res, fmt.Errorf("generate config: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, maxRunDuration)
	defer cancel()

	cmd := exec.CommandContext(runCtx, matchaBinary, "-c", generatedConfigPath())
	// Put matcha in its own process group so a timeout kills anything it
	// spawned, not just matcha itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = killGraceDelay

	tb := &tailBuffer{maxBytes: maxRunLogBytes}
	var sink io.Writer = tb
	// Stream to last-run.log as output arrives so a hung run is still
	// inspectable while it is still hung.
	if lf, err := os.OpenFile(lastRunLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		defer lf.Close()
		sink = io.MultiWriter(tb, lf)
	} else {
		log.Printf("could not open %s: %v", lastRunLogPath(), err)
	}
	cmd.Stdout = sink
	cmd.Stderr = sink

	log.Printf("starting matcha (source: %s)", source)
	runErr := cmd.Run()

	res.FinishedAt = time.Now()
	res.DurationSec = res.FinishedAt.Sub(started).Seconds()
	res.OutputTail = tail(tb.String(), maxOutputTail)
	res.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)

	switch {
	case runErr == nil:
		res.OK = true
		res.ExitCode = 0
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if res.TimedOut {
			res.Error = fmt.Sprintf("run exceeded the %s limit and was stopped", maxRunDuration)
		} else {
			res.Error = runErr.Error()
		}
	}

	writeLastRun(res)
	log.Printf("matcha finished in %.1fs (exit %d, ok=%v)", res.DurationSec, res.ExitCode, res.OK)
	return res, nil
}

func acquireRunLock() (*os.File, error) {
	f, err := os.OpenFile(runLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open run lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRunInProgress
		}
		return nil, fmt.Errorf("lock %s: %w", runLockPath(), err)
	}
	return f, nil
}

func releaseRunLock(f *os.File) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

func writeLastRun(res RunResult) {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		log.Printf("could not encode last-run record: %v", err)
		return
	}
	if err := writeFileAtomic(lastRunPath(), append(data, '\n'), 0o644); err != nil {
		log.Printf("could not write %s: %v", lastRunPath(), err)
	}
}

func readLastRun() *RunResult {
	data, err := os.ReadFile(lastRunPath())
	if err != nil {
		return nil
	}
	var res RunResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil
	}
	return &res
}
