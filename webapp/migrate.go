package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Keys we knowingly map into Settings. Anything else in a user's config.yaml
// gets a migration warning rather than being dropped silently.
var knownLegacyKeys = map[string]bool{
	"markdown_dir_path":        true, // pinned, intentionally ignored
	"database_file_path":       true, // pinned, intentionally ignored
	"terminal_mode":            true, // forced off, intentionally ignored
	"markdown_file_prefix":     true,
	"markdown_file_suffix":     true,
	"feeds":                    true,
	"summary_feeds":            true,
	"analyst_feeds":            true,
	"google_news_keywords":     true,
	"opml_file_path":           true,
	"instapaper":               true,
	"reading_time":             true,
	"show_images":              true,
	"sunrise_sunset":           true,
	"weather_latitude":         true,
	"weather_longitude":        true,
	"openai_api_key":           true,
	"openai_base_url":          true,
	"openai_model":             true,
	"summary_prompt":           true,
	"analyst_prompt":           true,
	"analyst_model":            true,
	"notification_trigger":     true,
	"notification_webhook_url": true,
}

// asString coerces scalars the way viper would: numbers and booleans written
// without quotes still read back as strings.
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "y", "on", "1":
			return true
		}
	case int:
		return t != 0
	case float64:
		return t != 0
	}
	return false
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return f
		}
	}
	return 0
}

// asStringList accepts a YAML sequence, and also a bare scalar. The scalar
// case matters: `feeds: http://example.com` is an unchecked type assertion in
// matcha and panics the run, so migration is the right moment to repair it.
func asStringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s := asString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	case nil:
		return nil
	default:
		if s := asString(v); s != "" {
			return []string{s}
		}
	}
	return nil
}

// parseFeedEntry splits matcha's "<url> <limit>" form. The limit is optional,
// and a trailing token that is not a positive integer is treated as part of
// the URL rather than an error -- matcha would log.Fatalf on that input, so
// importing it as-is at least lets the user see and fix it in the UI.
func parseFeedEntry(entry string) Feed {
	entry = strings.TrimSpace(entry)
	f := Feed{URL: entry, Enabled: true}

	idx := strings.LastIndex(entry, " ")
	if idx <= 0 {
		return f
	}
	head := strings.TrimSpace(entry[:idx])
	tail := strings.TrimSpace(entry[idx+1:])
	n, err := strconv.Atoi(tail)
	if err != nil || n <= 0 || head == "" {
		return f
	}
	f.URL = head
	f.Limit = n
	return f
}

func parseFeedList(v any) []Feed {
	entries := asStringList(v)
	out := make([]Feed, 0, len(entries))
	for _, e := range entries {
		out = append(out, parseFeedEntry(e))
	}
	return out
}

// migrateLegacyConfig converts a parsed config.yaml into Settings. Exported
// separately from the file handling so it can be tested directly.
func migrateLegacyConfig(raw map[string]any) (Settings, []string) {
	s := defaultSettings()
	var warnings []string

	get := func(key string) (any, bool) {
		v, ok := raw[key]
		return v, ok
	}

	if v, ok := get("feeds"); ok {
		s.Feeds = parseFeedList(v)
	}
	if v, ok := get("summary_feeds"); ok {
		s.SummaryFeeds = parseFeedList(v)
		for i := range s.SummaryFeeds {
			s.SummaryFeeds[i].Limit = 0
		}
	}
	if v, ok := get("analyst_feeds"); ok {
		s.AnalystFeeds = parseFeedList(v)
		for i := range s.AnalystFeeds {
			s.AnalystFeeds[i].Limit = 0
		}
	}

	if v, ok := get("google_news_keywords"); ok {
		for _, part := range strings.Split(asString(v), ",") {
			if part = strings.TrimSpace(part); part != "" {
				s.GoogleNewsKeywords = append(s.GoogleNewsKeywords, part)
			}
		}
	}

	if v, ok := get("markdown_file_prefix"); ok {
		s.MarkdownFilePrefix = asString(v)
	}
	if v, ok := get("markdown_file_suffix"); ok {
		s.MarkdownFileSuffix = asString(v)
	}
	if v, ok := get("opml_file_path"); ok {
		s.OPMLFilePath = asString(v)
	}

	if v, ok := get("instapaper"); ok {
		s.Instapaper = asBool(v)
	}
	if v, ok := get("reading_time"); ok {
		s.ReadingTime = asBool(v)
	}
	if v, ok := get("show_images"); ok {
		s.ShowImages = asBool(v)
	}
	if v, ok := get("sunrise_sunset"); ok {
		s.SunriseSunset = asBool(v)
	}
	if v, ok := get("weather_latitude"); ok {
		s.WeatherLat = asFloat(v)
	}
	if v, ok := get("weather_longitude"); ok {
		s.WeatherLon = asFloat(v)
	}

	if v, ok := get("openai_api_key"); ok {
		s.OpenAIAPIKey = asString(v)
	}
	if v, ok := get("openai_base_url"); ok {
		s.OpenAIBaseURL = asString(v)
	}
	if v, ok := get("openai_model"); ok {
		s.OpenAIModel = asString(v)
	}
	if v, ok := get("summary_prompt"); ok {
		s.SummaryPrompt = asString(v)
	}
	if v, ok := get("analyst_prompt"); ok {
		s.AnalystPrompt = asString(v)
	}
	if v, ok := get("analyst_model"); ok {
		s.AnalystModel = asString(v)
	}
	if v, ok := get("notification_trigger"); ok {
		s.NotificationTrigger = asString(v)
	}
	if v, ok := get("notification_webhook_url"); ok {
		s.NotificationWebhookURL = asString(v)
	}

	if v, ok := get("markdown_dir_path"); ok {
		if p := asString(v); p != "" && strings.TrimSuffix(p, "/") != strings.TrimSuffix(pinnedMarkdownDir, "/") {
			warnings = append(warnings, fmt.Sprintf(
				"markdown_dir_path was %q; digests are now always written to %s.", p, pinnedMarkdownDir))
		}
	}
	if v, ok := get("database_file_path"); ok {
		if p := asString(v); p != "" && p != pinnedDatabaseFile {
			warnings = append(warnings, fmt.Sprintf(
				"database_file_path was %q; matcha's history database is now always at %s. "+
					"Copy the old file there if you want to keep your read history.", p, pinnedDatabaseFile))
		}
	}
	if v, ok := get("terminal_mode"); ok && asBool(v) {
		warnings = append(warnings,
			"terminal_mode was enabled, which makes matcha print to the console and write no files. It is now always off.")
	}

	var unknown []string
	for k := range raw {
		if !knownLegacyKeys[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		warnings = append(warnings, fmt.Sprintf(
			"Unrecognised setting %q was not imported. The original file is kept at %s.",
			k, legacyConfigBackupPath()))
	}

	return s, warnings
}

// ensureSettings loads the settings store, importing a pre-existing
// config.yaml the first time so users do not lose their feeds on upgrade.
func ensureSettings(st *settingsStore) error {
	err := st.Load()
	if err == nil {
		return nil
	}
	if err != errNoSettings {
		return err
	}

	legacyPath := generatedConfigPath()
	data, readErr := os.ReadFile(legacyPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return fmt.Errorf("read %s: %w", legacyPath, readErr)
		}
		log.Printf("no existing configuration found; starting with defaults")
		return st.saveInitial(defaultSettings())
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// An unparseable legacy config should not block startup; keep it and
		// start fresh so the user can look at the backup and re-enter feeds.
		log.Printf("WARNING: could not parse existing %s (%v); starting with defaults", legacyPath, err)
		s := defaultSettings()
		s.MigrationWarnings = []string{fmt.Sprintf(
			"Your previous config.yaml could not be parsed (%v) and was not imported. It is kept at %s.",
			err, legacyConfigBackupPath())}
		if err := st.saveInitial(s); err != nil {
			return err
		}
		return backupLegacyConfig(legacyPath)
	}

	s, warnings := migrateLegacyConfig(raw)
	s.MigrationWarnings = warnings

	if err := st.saveInitial(s); err != nil {
		return err
	}
	log.Printf("imported %d feeds from existing config.yaml (%d warnings)", len(s.Feeds), len(warnings))
	return backupLegacyConfig(legacyPath)
}

// backupLegacyConfig moves the user's hand-written config aside. It is renamed
// rather than deleted because it holds comments and any keys we did not
// import, and the very next thing we do is overwrite config.yaml.
func backupLegacyConfig(path string) error {
	if err := os.Rename(path, legacyConfigBackupPath()); err != nil {
		return fmt.Errorf("back up %s: %w", path, err)
	}
	log.Printf("previous config.yaml saved as %s", legacyConfigBackupPath())
	return nil
}
