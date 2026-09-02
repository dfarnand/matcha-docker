package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser accepts the standard 5-field syntax plus @daily/@hourly style
// descriptors, matching what users already have in CRON_SCHEDULE.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ValidateCronSchedule reports whether spec is a schedule we can run.
func ValidateCronSchedule(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return errors.New("a schedule is required")
	}
	if _, err := cronParser.Parse(spec); err != nil {
		return errors.New(cronErrorMessage(spec, err))
	}
	return nil
}

// cronErrorMessage turns the parser's terse output into something a user
// staring at a text box can act on.
func cronErrorMessage(spec string, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "expected exactly 5 fields") ||
		strings.Contains(msg, "expected 5 to 6 fields") {
		return "expected 5 fields: minute hour day-of-month month day-of-week (for example \"0 6 * * *\")"
	}
	if strings.HasPrefix(spec, "@") {
		return msg + " (supported shortcuts are @hourly, @daily, @weekly, @monthly, @yearly and @every <duration>)"
	}
	return msg
}

// parseSchedule returns a runnable schedule for spec.
func parseSchedule(spec string) (cron.Schedule, error) {
	return cronParser.Parse(strings.TrimSpace(spec))
}

// scheduler runs matcha on the user's schedule from inside the webapp process.
//
// This replaces the container's cron daemon. The webapp is already PID 1 and
// already owns the run logic, so an in-process timer removes a daemon, removes
// writing to /etc/crontabs from a root web process, and lets a schedule change
// take effect immediately instead of waiting for a crontab re-read.
//
// The trade-off is that scheduled digests now depend on this process staying
// alive. It is PID 1 under `restart: unless-stopped`, so a crash restarts the
// container rather than silently stopping digests.
type scheduler struct {
	mu    sync.Mutex
	sched cron.Schedule
	spec  string
	next  time.Time

	reload chan struct{}
	run    func(ctx context.Context, source string) (RunResult, error)
}

func newScheduler(spec string, run func(context.Context, string) (RunResult, error)) (*scheduler, error) {
	s, err := parseSchedule(spec)
	if err != nil {
		return nil, err
	}
	return &scheduler{
		sched:  s,
		spec:   spec,
		reload: make(chan struct{}, 1),
		run:    run,
	}, nil
}

// Update swaps in a new schedule and wakes the loop so the change is visible
// immediately rather than after the currently pending sleep.
func (sc *scheduler) Update(spec string) error {
	s, err := parseSchedule(spec)
	if err != nil {
		return err
	}
	sc.mu.Lock()
	unchanged := sc.spec == spec
	sc.sched = s
	sc.spec = spec
	sc.mu.Unlock()

	if unchanged {
		return nil
	}
	select {
	case sc.reload <- struct{}{}:
	default: // a reload is already pending; it will pick up the new schedule
	}
	return nil
}

// NextRun reports when the next scheduled run is due, or the zero time if the
// loop has not computed one yet.
func (sc *scheduler) NextRun() time.Time {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.next
}

func (sc *scheduler) setNext(t time.Time) {
	sc.mu.Lock()
	sc.next = t
	sc.mu.Unlock()
}

func (sc *scheduler) current() cron.Schedule {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.sched
}

// Run blocks until ctx is cancelled, triggering matcha on schedule.
func (sc *scheduler) Run(ctx context.Context) {
	for {
		next := sc.current().Next(time.Now())
		sc.setNext(next)

		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}
		log.Printf("next scheduled run at %s (in %s)", next.Format(time.RFC3339), delay.Round(time.Second))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-sc.reload:
			// Schedule changed; recompute against the new one.
			timer.Stop()
			continue
		case <-timer.C:
		}

		if _, err := sc.run(ctx, runSourceSchedule); err != nil {
			if errors.Is(err, ErrRunInProgress) {
				log.Printf("scheduled run skipped: a run is already in progress")
			} else {
				log.Printf("scheduled run failed: %v", err)
			}
		}
		// Recomputing from time.Now() after the run finishes also guarantees
		// we never fire twice for the same instant when a run is quick.
	}
}
