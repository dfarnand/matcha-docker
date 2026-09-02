package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerateConfigPinsPaths(t *testing.T) {
	out, err := GenerateConfig(Settings{Schedule: defaultSchedule})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("generated config is not valid YAML: %v\n%s", err, out)
	}

	if got["markdown_dir_path"] != pinnedMarkdownDir {
		t.Errorf("markdown_dir_path = %v, want %q", got["markdown_dir_path"], pinnedMarkdownDir)
	}
	if got["database_file_path"] != pinnedDatabaseFile {
		t.Errorf("database_file_path = %v, want %q", got["database_file_path"], pinnedDatabaseFile)
	}
	// terminal_mode makes matcha print to stdout and write no files, so it
	// must never appear in a generated config.
	if _, ok := got["terminal_mode"]; ok {
		t.Error("terminal_mode must not be emitted")
	}
	if !strings.HasPrefix(string(out), "# GENERATED FILE") {
		t.Error("generated config should start with the do-not-edit header")
	}
}

func TestGenerateConfigFeedLimitsAndEnabled(t *testing.T) {
	s := Settings{
		Schedule: defaultSchedule,
		Feeds: []Feed{
			{URL: "http://hnrss.org/best", Limit: 10, Enabled: true},
			{URL: "https://waitbutwhy.com/feed", Enabled: true},
			{URL: "https://muted.example/feed", Limit: 5, Enabled: false},
		},
		AnalystFeeds: []Feed{{URL: "https://bbc.example/business.xml", Limit: 99, Enabled: true}},
	}

	out, err := GenerateConfig(s)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var got matchaConfig
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{"http://hnrss.org/best 10", "https://waitbutwhy.com/feed"}
	if len(got.Feeds) != len(want) {
		t.Fatalf("feeds = %v, want %v", got.Feeds, want)
	}
	for i := range want {
		if got.Feeds[i] != want[i] {
			t.Errorf("feeds[%d] = %q, want %q", i, got.Feeds[i], want[i])
		}
	}
	// matcha hardcodes 20 items for analyst feeds; emitting a suffix there
	// would be parsed as part of the URL.
	if len(got.AnalystFeeds) != 1 || got.AnalystFeeds[0] != "https://bbc.example/business.xml" {
		t.Errorf("analyst_feeds = %v, want no limit suffix", got.AnalystFeeds)
	}
}

func TestGenerateConfigJoinsKeywords(t *testing.T) {
	out, err := GenerateConfig(Settings{
		Schedule:           defaultSchedule,
		GoogleNewsKeywords: []string{"Anthropic", "Unraid", "Copenhagen"},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var got matchaConfig
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.GoogleNewsKeywords != "Anthropic,Unraid,Copenhagen" {
		t.Errorf("google_news_keywords = %q", got.GoogleNewsKeywords)
	}
}

// Free-text prompts are exactly why generation goes through yaml.Marshal
// instead of string concatenation.
func TestGenerateConfigQuotesAwkwardPrompts(t *testing.T) {
	nasty := "Summarize: \"this\" & that\nAcross lines: with: colons #hash"
	out, err := GenerateConfig(Settings{Schedule: defaultSchedule, SummaryPrompt: nasty})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var got matchaConfig
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("generated config with an awkward prompt does not parse: %v\n%s", err, out)
	}
	if got.SummaryPrompt != nasty {
		t.Errorf("summary_prompt round-trip mismatch:\ngot  %q\nwant %q", got.SummaryPrompt, nasty)
	}
}

func TestMaskSecretsInConfig(t *testing.T) {
	text := "openai_api_key: sk-secret-value\n"
	if got := maskSecretsInConfig(text, "sk-secret-value"); strings.Contains(got, "sk-secret-value") {
		t.Errorf("key was not masked: %q", got)
	}
	if got := maskSecretsInConfig(text, ""); got != text {
		t.Errorf("empty key should leave text untouched")
	}
}
