package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Container paths. These are vars, and overridable by environment variable,
// so the binary can be run outside the image (tests, local development).
// Inside the container the defaults are correct and nothing sets these.
var (
	configDir = envOr("MATCHA_CONFIG_DIR", "/app/config")
	staticDir = envOr("MATCHA_STATIC_DIR", "/app/webapp/static")
)

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func settingsPath() string   { return filepath.Join(configDir, "settings.json") }
func authPath() string       { return filepath.Join(configDir, "auth.json") }
func initialPwPath() string  { return filepath.Join(configDir, "initial-password.txt") }
func lastRunPath() string    { return filepath.Join(configDir, "last-run.json") }
func lastRunLogPath() string { return filepath.Join(configDir, "last-run.log") }
func generatedConfigPath() string {
	return filepath.Join(configDir, "config.yaml")
}
func legacyConfigBackupPath() string {
	return filepath.Join(configDir, "config.yaml.pre-webapp.bak")
}
func runLockPath() string { return filepath.Join(configDir, ".matcha-run.lock") }

const (
	defaultSchedule = "0 6 * * *"

	// secretUnchanged is what the browser sends back for a secret it never
	// received: an empty string means "leave the stored value alone".
	// secretClear is the explicit "wipe it" sentinel.
	secretClear = "__MATCHA_CLEAR__"
)

// Feed is one entry in feeds/summary_feeds/analyst_feeds. Limit applies only
// to feeds; matcha hardcodes 20 for analyst_feeds and we do not offer it for
// summary_feeds. Limit 0 means "unset" and generates a bare URL.
type Feed struct {
	URL     string `json:"url"`
	Limit   int    `json:"limit,omitempty"`
	Enabled bool   `json:"enabled"`
}

// Settings is the source of truth for matcha's configuration. It deliberately
// has no markdown_dir_path, database_file_path or terminal_mode field: those
// are pinned constants in config_gen.go, not user data.
type Settings struct {
	Version  int    `json:"version"`
	Schedule string `json:"schedule"`

	Feeds        []Feed `json:"feeds"`
	SummaryFeeds []Feed `json:"summary_feeds"`
	AnalystFeeds []Feed `json:"analyst_feeds"`

	GoogleNewsKeywords []string `json:"google_news_keywords"`

	MarkdownFilePrefix string `json:"markdown_file_prefix"`
	MarkdownFileSuffix string `json:"markdown_file_suffix"`

	Instapaper    bool    `json:"instapaper"`
	ReadingTime   bool    `json:"reading_time"`
	ShowImages    bool    `json:"show_images"`
	SunriseSunset bool    `json:"sunrise_sunset"`
	WeatherLat    float64 `json:"weather_latitude"`
	WeatherLon    float64 `json:"weather_longitude"`

	OPMLFilePath string `json:"opml_file_path"`

	OpenAIAPIKey  string `json:"openai_api_key"`
	OpenAIBaseURL string `json:"openai_base_url"`
	OpenAIModel   string `json:"openai_model"`
	SummaryPrompt string `json:"summary_prompt"`

	AnalystPrompt string `json:"analyst_prompt"`
	AnalystModel  string `json:"analyst_model"`

	NotificationTrigger    string `json:"notification_trigger"`
	NotificationWebhookURL string `json:"notification_webhook_url"`

	MigrationWarnings    []string `json:"migration_warnings"`
	MigrationWarningsAck bool     `json:"migration_warnings_ack"`
}

func defaultSettings() Settings {
	schedule := strings.TrimSpace(os.Getenv("CRON_SCHEDULE"))
	if schedule == "" {
		schedule = defaultSchedule
	}
	if err := ValidateCronSchedule(schedule); err != nil {
		// A bad CRON_SCHEDULE must not leave the instance unschedulable.
		schedule = defaultSchedule
	}
	return Settings{
		Version:  1,
		Schedule: schedule,
	}
}

// clone deep-copies the slice fields. Handing out the stored Settings with its
// slices aliased would let a caller mutate the store in place, which matters
// because the settings UI reorders feeds.
func (s Settings) clone() Settings {
	out := s
	out.Feeds = append([]Feed(nil), s.Feeds...)
	out.SummaryFeeds = append([]Feed(nil), s.SummaryFeeds...)
	out.AnalystFeeds = append([]Feed(nil), s.AnalystFeeds...)
	out.GoogleNewsKeywords = append([]string(nil), s.GoogleNewsKeywords...)
	out.MigrationWarnings = append([]string(nil), s.MigrationWarnings...)
	return out
}

// normalize trims incoming values and drops empties. Run before validation so
// that a feed row the user left blank is removed rather than reported.
func (s *Settings) normalize() {
	s.Version = 1
	s.Schedule = strings.TrimSpace(s.Schedule)

	normFeeds := func(in []Feed, allowLimit bool) []Feed {
		out := make([]Feed, 0, len(in))
		for _, f := range in {
			f.URL = strings.TrimSpace(f.URL)
			if f.URL == "" {
				continue
			}
			if !allowLimit {
				f.Limit = 0
			}
			if f.Limit < 0 {
				f.Limit = 0
			}
			out = append(out, f)
		}
		return out
	}
	s.Feeds = normFeeds(s.Feeds, true)
	s.SummaryFeeds = normFeeds(s.SummaryFeeds, false)
	s.AnalystFeeds = normFeeds(s.AnalystFeeds, false)

	kw := make([]string, 0, len(s.GoogleNewsKeywords))
	for _, k := range s.GoogleNewsKeywords {
		if k = strings.TrimSpace(k); k != "" {
			kw = append(kw, k)
		}
	}
	s.GoogleNewsKeywords = kw

	s.OPMLFilePath = strings.TrimSpace(s.OPMLFilePath)
	s.OpenAIBaseURL = strings.TrimSpace(s.OpenAIBaseURL)
	s.OpenAIModel = strings.TrimSpace(s.OpenAIModel)
	s.AnalystModel = strings.TrimSpace(s.AnalystModel)
	s.OpenAIAPIKey = strings.TrimSpace(s.OpenAIAPIKey)
	s.NotificationWebhookURL = strings.TrimSpace(s.NotificationWebhookURL)
}

// ValidationError points the UI at the specific control that is wrong.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationResult separates hard errors (block the save) from warnings
// (surfaced inline, save proceeds).
type ValidationResult struct {
	Errors   []ValidationError `json:"errors"`
	Warnings []string          `json:"warnings"`
}

func (v ValidationResult) OK() bool { return len(v.Errors) == 0 }

func (v ValidationResult) Error() string {
	parts := make([]string, 0, len(v.Errors))
	for _, e := range v.Errors {
		parts = append(parts, e.Field+": "+e.Message)
	}
	return strings.Join(parts, "; ")
}

// ValidateSettings enforces the constraints matcha itself does not: it will
// log.Fatalf on a feed URL containing a space, and panics on a few other
// malformed shapes. Everything rejected here would otherwise surface as a
// failed digest run hours later.
func ValidateSettings(s Settings) ValidationResult {
	var res ValidationResult
	addErr := func(field, msg string) {
		res.Errors = append(res.Errors, ValidationError{Field: field, Message: msg})
	}

	if s.Schedule == "" {
		addErr("schedule", "a schedule is required")
	} else if err := ValidateCronSchedule(s.Schedule); err != nil {
		addErr("schedule", err.Error())
	}

	checkFeeds := func(list []Feed, field string, allowLimit bool) {
		for i, f := range list {
			key := fmt.Sprintf("%s[%d]", field, i)

			// matcha splits each feed entry on whitespace and treats the
			// second token as an item count, so any embedded whitespace
			// either mis-parses the URL or aborts the run outright.
			if strings.ContainsAny(f.URL, " \t\n\r") {
				addErr(key, "URL must not contain spaces")
				continue
			}
			u, err := url.Parse(f.URL)
			if err != nil {
				addErr(key, "not a valid URL: "+err.Error())
				continue
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				addErr(key, "URL must start with http:// or https://")
				continue
			}
			if u.Host == "" {
				addErr(key, "URL is missing a hostname")
				continue
			}
			if allowLimit && (f.Limit < 0 || f.Limit > 1000) {
				addErr(key, "item limit must be between 1 and 1000 (or empty for the default)")
			}
		}
	}
	checkFeeds(s.Feeds, "feeds", true)
	checkFeeds(s.SummaryFeeds, "summary_feeds", false)
	checkFeeds(s.AnalystFeeds, "analyst_feeds", false)

	for i, k := range s.GoogleNewsKeywords {
		// The keys are joined with commas into a single matcha config value,
		// so a comma inside one would silently split it into two keywords.
		if strings.Contains(k, ",") {
			addErr(fmt.Sprintf("google_news_keywords[%d]", i),
				"keywords must not contain commas; add them as separate keywords")
		}
	}

	if s.OPMLFilePath != "" {
		clean := filepath.Clean(s.OPMLFilePath)
		if !filepath.IsAbs(clean) {
			addErr("opml_file_path", "must be an absolute path")
		} else if clean != configDir && !strings.HasPrefix(clean, configDir+string(filepath.Separator)) {
			addErr("opml_file_path", "must be inside "+configDir+" (the mounted config directory)")
		}
	}

	if s.WeatherLat < -90 || s.WeatherLat > 90 {
		addErr("weather_latitude", "must be between -90 and 90")
	}
	if s.WeatherLon < -180 || s.WeatherLon > 180 {
		addErr("weather_longitude", "must be between -180 and 180")
	}

	if s.NotificationWebhookURL != "" {
		u, err := url.Parse(s.NotificationWebhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			addErr("notification_webhook_url", "must be an http:// or https:// URL")
		}
	}

	// Warnings: valid configurations that will not do what the user expects.
	if s.SunriseSunset && (s.WeatherLat == 0 || s.WeatherLon == 0) {
		res.Warnings = append(res.Warnings,
			"Sunrise/sunset needs both latitude and longitude to be non-zero.")
	}
	if s.ReadingTime {
		res.Warnings = append(res.Warnings,
			"Reading time fetches the full text of every article, which makes runs much slower.")
	}
	needsKey := len(s.SummaryFeeds) > 0 || s.AnalystPrompt != ""
	if needsKey && s.OpenAIAPIKey == "" {
		res.Warnings = append(res.Warnings,
			"Summary feeds and the analyst need an OpenAI API key to do anything.")
	}
	if len(s.AnalystFeeds) > 0 && s.AnalystPrompt == "" {
		res.Warnings = append(res.Warnings,
			"Analyst feeds are ignored while the analyst prompt is empty.")
	}
	if s.NotificationTrigger != "" && s.NotificationWebhookURL == "" {
		res.Warnings = append(res.Warnings,
			"A notification trigger is set but there is no webhook URL to send it to.")
	}

	return res
}

// errNoSettings signals that no store exists yet, so migration should run.
var errNoSettings = errors.New("settings store does not exist")

type settingsStore struct {
	mu sync.RWMutex
	s  Settings

	// degraded is set when settings.json existed but could not be parsed. In
	// that state the file has been moved aside, defaults are serving, and
	// saves are refused until the user explicitly discards the old data. We
	// never silently overwrite: that file may hold dozens of hand-added feeds.
	degraded   string
	backupPath string

	// onChange runs after a successful save, outside the lock. main wires this
	// to regenerate config.yaml and nudge the scheduler.
	onChange func(Settings)
}

// Load reads settings.json. A missing file returns errNoSettings so the caller
// can run migration; a corrupt file is moved aside and reported via Degraded.
func (st *settingsStore) Load() error {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return errNoSettings
		}
		return fmt.Errorf("read %s: %w", settingsPath(), err)
	}

	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", settingsPath(), time.Now().Unix())
		if rnErr := os.Rename(settingsPath(), backup); rnErr != nil {
			return fmt.Errorf("settings.json is corrupt (%v) and could not be moved aside: %w", err, rnErr)
		}
		st.mu.Lock()
		st.s = defaultSettings()
		st.degraded = err.Error()
		st.backupPath = backup
		st.mu.Unlock()
		return nil
	}

	s.normalize()
	if s.Schedule == "" {
		s.Schedule = defaultSettings().Schedule
	}
	st.mu.Lock()
	st.s = s
	st.degraded = ""
	st.backupPath = ""
	st.mu.Unlock()
	return nil
}

func (st *settingsStore) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s.clone()
}

// Degraded reports the parse error and backup path when the store failed to
// load, or empty strings when everything is fine.
func (st *settingsStore) Degraded() (string, string) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.degraded, st.backupPath
}

// ClearDegraded accepts the loss of the unparseable file and re-enables saves.
func (st *settingsStore) ClearDegraded() {
	st.mu.Lock()
	st.degraded = ""
	st.backupPath = ""
	st.mu.Unlock()
}

// Save normalizes, validates and atomically persists s. Validation failures
// leave the store and the file untouched.
func (st *settingsStore) Save(s Settings) (ValidationResult, error) {
	s.normalize()
	res := ValidateSettings(s)
	if !res.OK() {
		return res, nil
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return res, fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')

	st.mu.Lock()
	if st.degraded != "" {
		st.mu.Unlock()
		return res, errors.New("settings are in degraded mode; discard the unreadable file before saving")
	}
	// settings.json holds the OpenAI API key, so it is owner-only.
	if err := writeFileAtomic(settingsPath(), data, 0o600); err != nil {
		st.mu.Unlock()
		return res, err
	}
	st.s = s
	onChange := st.onChange
	st.mu.Unlock()

	if onChange != nil {
		onChange(s.clone())
	}
	return res, nil
}

// saveInitial persists a freshly migrated or defaulted store without running
// the onChange hook (nothing is listening yet at startup) and without failing
// on validation warnings inherited from a legacy config.
func (st *settingsStore) saveInitial(s Settings) error {
	s.normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(settingsPath(), data, 0o600); err != nil {
		return err
	}
	st.mu.Lock()
	st.s = s
	st.mu.Unlock()
	return nil
}
