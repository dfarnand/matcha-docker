package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultPort = "7321"

type server struct {
	settings *settingsStore
	auth     *authStore
	guard    *authGuard
	runner   *runner
	sched    *scheduler
}

func main() {
	var (
		runNow    = flag.Bool("run", false, "run matcha once and exit")
		source    = flag.String("source", runSourceManualCLI, "how this run was triggered (recorded in last-run.json)")
		genConfig = flag.Bool("generate-config", false, "print the generated matcha config and exit")
		outPath   = flag.String("o", "", "with -generate-config, write to this file instead of stdout")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags)

	switch {
	case *genConfig:
		os.Exit(runGenerateConfig(*outPath))
	case *runNow:
		os.Exit(runOnce(*source))
	default:
		os.Exit(runServer())
	}
}

// loadSettings prepares the settings store, importing a legacy config.yaml the
// first time. Shared by every mode.
func loadSettings() (*settingsStore, error) {
	st := &settingsStore{}
	if err := ensureSettings(st); err != nil {
		return nil, err
	}
	return st, nil
}

func runGenerateConfig(outPath string) int {
	st, err := loadSettings()
	if err != nil {
		log.Printf("error: %v", err)
		return 1
	}
	data, err := GenerateConfig(st.Get())
	if err != nil {
		log.Printf("error: %v", err)
		return 1
	}
	if outPath == "" {
		os.Stdout.Write(data)
		return 0
	}
	if err := writeFileAtomic(outPath, data, 0o600); err != nil {
		log.Printf("error: %v", err)
		return 1
	}
	return 0
}

// runOnce is what matcha-runner invokes. It shares RunMatcha with the
// scheduler so a cron-style run records the same status the UI shows.
func runOnce(source string) int {
	st, err := loadSettings()
	if err != nil {
		log.Printf("error: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := newRunner(st).Run(ctx, source)
	if err != nil {
		if errors.Is(err, ErrRunInProgress) {
			log.Printf("another matcha run is already in progress; skipping")
			return 0
		}
		log.Printf("error: %v", err)
		return 1
	}
	if !res.OK {
		return 1
	}
	return 0
}

func runServer() int {
	st, err := loadSettings()
	if err != nil {
		log.Printf("fatal: %v", err)
		return 1
	}

	as := &authStore{}
	guard := &authGuard{store: as, limiter: newRateLimiter()}
	if err := as.Load(); err != nil {
		// Fail closed: the settings area becomes unavailable, but digest
		// rendering keeps working. Regenerating credentials here would turn a
		// corrupt file into a free takeover.
		log.Printf("ERROR: %v", err)
		log.Printf("the settings area is disabled until this is resolved")
		guard.loadErr = err
	}

	r := newRunner(st)

	sched, err := newScheduler(st.Get().Schedule, r.Run)
	if err != nil {
		log.Printf("stored schedule %q is invalid (%v); falling back to %q",
			st.Get().Schedule, err, defaultSchedule)
		sched, err = newScheduler(defaultSchedule, r.Run)
		if err != nil {
			log.Printf("fatal: %v", err)
			return 1
		}
	}

	srv := &server{settings: st, auth: as, guard: guard, runner: r, sched: sched}

	// Keep config.yaml in step with the settings so the host-visible file is
	// always inspectable. The run path regenerates it anyway, so a failure
	// here is a warning rather than an error.
	st.onChange = func(s Settings) {
		if err := WriteConfig(s, generatedConfigPath()); err != nil {
			log.Printf("WARNING: could not write %s: %v", generatedConfigPath(), err)
		}
		if err := sched.Update(s.Schedule); err != nil {
			log.Printf("WARNING: could not apply schedule %q: %v", s.Schedule, err)
		}
	}
	if err := WriteConfig(st.Get(), generatedConfigPath()); err != nil {
		log.Printf("WARNING: could not write %s: %v", generatedConfigPath(), err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go sched.Run(ctx)

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("server starting on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Printf("fatal: %v", err)
		return 1
	case <-ctx.Done():
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
		return 0
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: the digest viewer and the PWA assets. Leaving these open keeps
	// the service worker and offline caching working exactly as before.
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /file/{name}", handleFile)
	mux.HandleFunc("GET /files", filesHandler)

	// Authentication.
	mux.HandleFunc("GET /login", s.guard.handleLoginPage)
	mux.HandleFunc("POST /login", s.guard.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.guard.requireAuth(s.guard.requireCSRF(s.guard.handleLogout)))

	// Everything below requires a session.
	auth := s.guard.requireAuth
	csrf := s.guard.requireCSRF

	mux.HandleFunc("GET /settings", auth(s.handleSettingsPage))
	mux.HandleFunc("GET /api/settings", auth(s.handleGetSettings))
	mux.HandleFunc("POST /api/settings", auth(csrf(s.handleSaveSettings)))
	mux.HandleFunc("POST /api/settings/discard", auth(csrf(s.handleDiscardCorrupt)))
	mux.HandleFunc("GET /api/config-preview", auth(s.handleConfigPreview))
	mux.HandleFunc("POST /api/feed-preview", auth(csrf(handleFeedPreview)))
	mux.HandleFunc("POST /api/run", auth(csrf(s.handleRun)))
	mux.HandleFunc("GET /api/run/status", auth(s.handleRunStatus))
	mux.HandleFunc("POST /api/password", auth(csrf(s.guard.handleChangePassword)))

	// Anything unmatched falls back to the digest, preserving the previous
	// behaviour where an unknown path showed the latest file.
	mux.HandleFunc("/", handleIndex)

	return mux
}

type settingsPageData struct {
	CSRFToken      string
	Degraded       string
	DegradedBackup string
}

func (s *server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	degraded, backup := s.settings.Degraded()
	render(w, "settings.html", settingsPageData{
		CSRFToken:      s.auth.csrfTokenFor(cookieValue(r, sessionCookieName)),
		Degraded:       degraded,
		DegradedBackup: backup,
	})
}

type settingsResponse struct {
	Settings       Settings `json:"settings"`
	APIKeySet      bool     `json:"openai_api_key_set"`
	Degraded       string   `json:"degraded,omitempty"`
	DegradedBackup string   `json:"degraded_backup,omitempty"`
	Timezone       string   `json:"timezone"`
}

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cur := s.settings.Get()
	apiKeySet := cur.OpenAIAPIKey != ""
	// Never round-trip the key to the browser: it would land in DevTools, the
	// page cache and any proxy in between for no benefit.
	cur.OpenAIAPIKey = ""

	degraded, backup := s.settings.Degraded()
	zone, _ := time.Now().Zone()

	writeJSON(w, http.StatusOK, settingsResponse{
		Settings:       cur,
		APIKeySet:      apiKeySet,
		Degraded:       degraded,
		DegradedBackup: backup,
		Timezone:       zone,
	})
}

type saveResponse struct {
	OK       bool              `json:"ok"`
	Errors   []ValidationError `json:"errors,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func (s *server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var incoming Settings
	if err := decodeJSONBody(w, r, &incoming); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read settings: "+err.Error())
		return
	}

	current := s.settings.Get()

	// The browser never receives the key, so an empty value means "leave it
	// alone" and an explicit sentinel means "wipe it".
	switch incoming.OpenAIAPIKey {
	case "":
		incoming.OpenAIAPIKey = current.OpenAIAPIKey
	case secretClear:
		incoming.OpenAIAPIKey = ""
	}

	// Migration warnings are server-owned; the client may only acknowledge them.
	if incoming.MigrationWarningsAck {
		incoming.MigrationWarnings = nil
	} else {
		incoming.MigrationWarnings = current.MigrationWarnings
	}

	res, err := s.settings.Save(incoming)
	if err != nil {
		writeJSON(w, http.StatusConflict, saveResponse{Error: err.Error()})
		return
	}
	if !res.OK() {
		writeJSON(w, http.StatusBadRequest, saveResponse{Errors: res.Errors, Warnings: res.Warnings})
		return
	}
	writeJSON(w, http.StatusOK, saveResponse{OK: true, Warnings: res.Warnings})
}

func (s *server) handleDiscardCorrupt(w http.ResponseWriter, r *http.Request) {
	s.settings.ClearDegraded()
	if err := s.settings.saveInitial(defaultSettings()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleConfigPreview(w http.ResponseWriter, r *http.Request) {
	cur := s.settings.Get()
	data, err := GenerateConfig(cur)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	text := string(data)
	if r.URL.Query().Get("reveal") != "1" {
		text = maskSecretsInConfig(text, cur.OpenAIAPIKey)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, text)
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	running, startedAt, _ := s.runner.Status()
	if running {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":         false,
			"status":     "busy",
			"started_at": startedAt,
			"error":      "A run is already in progress.",
		})
		return
	}

	// Never block the response on the run itself: with reading_time enabled a
	// run can outlast any reverse proxy's timeout.
	go func() {
		if _, err := s.runner.Run(context.Background(), runSourceManual); err != nil {
			log.Printf("manual run failed: %v", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "started"})
}

type runStatusResponse struct {
	Running   bool       `json:"running"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	NextRun   *time.Time `json:"next_run,omitempty"`
	Timezone  string     `json:"timezone"`
	Last      *RunResult `json:"last,omitempty"`
}

func (s *server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	running, startedAt, last := s.runner.Status()
	zone, _ := time.Now().Zone()

	resp := runStatusResponse{Running: running, Timezone: zone, Last: last}
	if running {
		resp.StartedAt = &startedAt
	}
	if next := s.sched.NextRun(); !next.IsZero() {
		resp.NextRun = &next
	}
	writeJSON(w, http.StatusOK, resp)
}
