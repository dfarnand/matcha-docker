package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseFeedEntry(t *testing.T) {
	cases := []struct {
		in        string
		wantURL   string
		wantLimit int
	}{
		{"http://hnrss.org/best 10", "http://hnrss.org/best", 10},
		{"https://waitbutwhy.com/feed", "https://waitbutwhy.com/feed", 0},
		{"  https://example.com/rss  ", "https://example.com/rss", 0},
		// A non-numeric trailing token is what makes matcha log.Fatalf. Keep
		// the whole string so the user can see and fix it in the UI.
		{"https://example.com/rss oops", "https://example.com/rss oops", 0},
		{"https://example.com/rss 0", "https://example.com/rss 0", 0},
		{"https://example.com/rss -5", "https://example.com/rss -5", 0},
	}
	for _, c := range cases {
		got := parseFeedEntry(c.in)
		if got.URL != c.wantURL || got.Limit != c.wantLimit {
			t.Errorf("parseFeedEntry(%q) = {%q, %d}, want {%q, %d}",
				c.in, got.URL, got.Limit, c.wantURL, c.wantLimit)
		}
		if !got.Enabled {
			t.Errorf("parseFeedEntry(%q) should be enabled", c.in)
		}
	}
}

func TestAsStringListAcceptsBareScalar(t *testing.T) {
	// `feeds: <url>` (a scalar instead of a list) is an unchecked type
	// assertion in matcha and panics the run.
	got := asStringList("https://example.com/feed")
	if len(got) != 1 || got[0] != "https://example.com/feed" {
		t.Errorf("asStringList(scalar) = %v", got)
	}
	if got := asStringList([]any{"a", "b"}); len(got) != 2 {
		t.Errorf("asStringList(seq) = %v", got)
	}
	if got := asStringList(nil); got != nil {
		t.Errorf("asStringList(nil) = %v", got)
	}
}

func TestAsBoolAndFloatCoercion(t *testing.T) {
	for _, v := range []any{true, "true", "yes", "on", 1, "1"} {
		if !asBool(v) {
			t.Errorf("asBool(%#v) = false, want true", v)
		}
	}
	for _, v := range []any{false, "false", "no", 0, "", "banana"} {
		if asBool(v) {
			t.Errorf("asBool(%#v) = true, want false", v)
		}
	}
	if asFloat("55.68") != 55.68 {
		t.Errorf("asFloat(string) failed")
	}
	if asFloat(12) != 12 {
		t.Errorf("asFloat(int) failed")
	}
}

func TestMigrateLegacyConfig(t *testing.T) {
	raw := map[string]any{
		"markdown_dir_path":    "/somewhere/else",
		"database_file_path":   "/root/.config/brew/matcha.db",
		"terminal_mode":        true,
		"feeds":                []any{"http://hnrss.org/best 10", "https://waitbutwhy.com/feed"},
		"google_news_keywords": "George Hotz, ChatGPT ,Copenhagen",
		"instapaper":           true,
		"weather_latitude":     55.68,
		"weather_longitude":    12.57,
		"openai_api_key":       "sk-test",
		"summary_prompt":       "Summarize:",
		"nonsense_key":         "whatever",
	}

	s, warnings := migrateLegacyConfig(raw)

	if len(s.Feeds) != 2 {
		t.Fatalf("feeds = %v", s.Feeds)
	}
	if s.Feeds[0].URL != "http://hnrss.org/best" || s.Feeds[0].Limit != 10 {
		t.Errorf("feeds[0] = %+v", s.Feeds[0])
	}
	wantKeywords := []string{"George Hotz", "ChatGPT", "Copenhagen"}
	if len(s.GoogleNewsKeywords) != 3 {
		t.Fatalf("keywords = %v", s.GoogleNewsKeywords)
	}
	for i, w := range wantKeywords {
		if s.GoogleNewsKeywords[i] != w {
			t.Errorf("keywords[%d] = %q, want %q", i, s.GoogleNewsKeywords[i], w)
		}
	}
	if !s.Instapaper || s.WeatherLat != 55.68 || s.OpenAIAPIKey != "sk-test" {
		t.Errorf("scalar import failed: %+v", s)
	}

	// The three pinned/forced keys and the unknown key should all be flagged.
	if len(warnings) != 4 {
		t.Errorf("expected 4 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestEnsureSettingsImportsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	old := configDir
	configDir = dir
	defer func() { configDir = old }()

	legacy := "feeds:\n  - http://hnrss.org/best 10\ninstapaper: true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	st := &settingsStore{}
	if err := ensureSettings(st); err != nil {
		t.Fatalf("ensureSettings: %v", err)
	}

	got := st.Get()
	if len(got.Feeds) != 1 || got.Feeds[0].Limit != 10 || !got.Instapaper {
		t.Errorf("imported settings wrong: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Errorf("settings.json not written: %v", err)
	}
	// The original must be preserved, not deleted: it holds comments and any
	// keys we did not import.
	if _, err := os.Stat(legacyConfigBackupPath()); err != nil {
		t.Errorf("legacy config was not backed up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("legacy config.yaml should have been renamed away")
	}

	// settings.json must be owner-only: it carries the OpenAI key.
	info, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("settings.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestEnsureSettingsNoLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	old := configDir
	configDir = dir
	defer func() { configDir = old }()

	t.Setenv("CRON_SCHEDULE", "*/15 * * * *")

	st := &settingsStore{}
	if err := ensureSettings(st); err != nil {
		t.Fatalf("ensureSettings: %v", err)
	}
	if got := st.Get().Schedule; got != "*/15 * * * *" {
		t.Errorf("schedule = %q, want the CRON_SCHEDULE seed", got)
	}
}

// A settings.json we cannot parse must be preserved, not silently replaced.
func TestLoadCorruptSettingsGoesDegraded(t *testing.T) {
	dir := t.TempDir()
	old := configDir
	configDir = dir
	defer func() { configDir = old }()

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := &settingsStore{}
	if err := st.Load(); err != nil {
		t.Fatalf("Load should degrade rather than fail: %v", err)
	}
	msg, backup := st.Degraded()
	if msg == "" || backup == "" {
		t.Fatal("expected degraded mode to be reported")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("corrupt file was not preserved at %s: %v", backup, err)
	}
	// Saving must be refused until the user explicitly discards the old data.
	if _, err := st.Save(defaultSettings()); err == nil {
		t.Error("Save should be refused while degraded")
	}
}

func TestGeneratedConfigRoundTripsThroughMigration(t *testing.T) {
	original := Settings{
		Schedule:           defaultSchedule,
		Feeds:              []Feed{{URL: "http://hnrss.org/best", Limit: 10, Enabled: true}},
		GoogleNewsKeywords: []string{"one", "two"},
		SummaryPrompt:      "Summarize: with a colon",
		Instapaper:         true,
	}
	out, err := GenerateConfig(original)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	back, _ := migrateLegacyConfig(raw)

	if len(back.Feeds) != 1 || back.Feeds[0].URL != "http://hnrss.org/best" || back.Feeds[0].Limit != 10 {
		t.Errorf("feed did not survive the round trip: %+v", back.Feeds)
	}
	if back.SummaryPrompt != original.SummaryPrompt {
		t.Errorf("prompt did not survive: %q", back.SummaryPrompt)
	}
	if len(back.GoogleNewsKeywords) != 2 {
		t.Errorf("keywords did not survive: %v", back.GoogleNewsKeywords)
	}
}
