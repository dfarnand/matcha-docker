package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Paths matcha must use inside the container. These are pinned rather than
// exposed as settings: markdown_dir_path has to match the output bind mount
// for the digest viewer to find anything, and database_file_path has to live
// on a volume or matcha's dedupe history evaporates on every rebuild and
// every article looks new again.
const (
	pinnedMarkdownDir  = "/app/output/"
	pinnedDatabaseFile = "/app/config/matcha.db"
)

// matchaConfig mirrors matcha's viper schema. Field order here is the order
// yaml.v3 emits, so it doubles as the layout of the generated file.
//
// terminal_mode is deliberately absent: it makes matcha print to stdout and
// write no files, which would silently produce no digest.
type matchaConfig struct {
	MarkdownDirPath  string `yaml:"markdown_dir_path"`
	DatabaseFilePath string `yaml:"database_file_path"`

	MarkdownFilePrefix string `yaml:"markdown_file_prefix,omitempty"`
	MarkdownFileSuffix string `yaml:"markdown_file_suffix,omitempty"`

	Feeds        []string `yaml:"feeds,omitempty"`
	SummaryFeeds []string `yaml:"summary_feeds,omitempty"`

	GoogleNewsKeywords string `yaml:"google_news_keywords,omitempty"`
	OPMLFilePath       string `yaml:"opml_file_path,omitempty"`

	Instapaper    bool    `yaml:"instapaper,omitempty"`
	ReadingTime   bool    `yaml:"reading_time,omitempty"`
	ShowImages    bool    `yaml:"show_images,omitempty"`
	SunriseSunset bool    `yaml:"sunrise_sunset,omitempty"`
	WeatherLat    float64 `yaml:"weather_latitude,omitempty"`
	WeatherLon    float64 `yaml:"weather_longitude,omitempty"`

	OpenAIAPIKey  string `yaml:"openai_api_key,omitempty"`
	OpenAIBaseURL string `yaml:"openai_base_url,omitempty"`
	OpenAIModel   string `yaml:"openai_model,omitempty"`
	SummaryPrompt string `yaml:"summary_prompt,omitempty"`

	AnalystFeeds  []string `yaml:"analyst_feeds,omitempty"`
	AnalystPrompt string   `yaml:"analyst_prompt,omitempty"`
	AnalystModel  string   `yaml:"analyst_model,omitempty"`

	NotificationTrigger    string `yaml:"notification_trigger,omitempty"`
	NotificationWebhookURL string `yaml:"notification_webhook_url,omitempty"`
}

// feedLines renders enabled feeds into matcha's "<url>" or "<url> <limit>"
// form. Disabled feeds are kept in settings.json but omitted here, which is
// how the UI mutes a noisy feed without losing the URL.
func feedLines(feeds []Feed, withLimit bool) []string {
	out := make([]string, 0, len(feeds))
	for _, f := range feeds {
		if !f.Enabled || f.URL == "" {
			continue
		}
		if withLimit && f.Limit > 0 {
			out = append(out, f.URL+" "+strconv.Itoa(f.Limit))
			continue
		}
		out = append(out, f.URL)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toMatchaConfig(s Settings) matchaConfig {
	return matchaConfig{
		MarkdownDirPath:  pinnedMarkdownDir,
		DatabaseFilePath: pinnedDatabaseFile,

		MarkdownFilePrefix: s.MarkdownFilePrefix,
		MarkdownFileSuffix: s.MarkdownFileSuffix,

		Feeds:        feedLines(s.Feeds, true),
		SummaryFeeds: feedLines(s.SummaryFeeds, false),

		// matcha wants a single comma-separated string here, not a list.
		GoogleNewsKeywords: strings.Join(s.GoogleNewsKeywords, ","),
		OPMLFilePath:       s.OPMLFilePath,

		Instapaper:    s.Instapaper,
		ReadingTime:   s.ReadingTime,
		ShowImages:    s.ShowImages,
		SunriseSunset: s.SunriseSunset,
		WeatherLat:    s.WeatherLat,
		WeatherLon:    s.WeatherLon,

		OpenAIAPIKey:  s.OpenAIAPIKey,
		OpenAIBaseURL: s.OpenAIBaseURL,
		OpenAIModel:   s.OpenAIModel,
		SummaryPrompt: s.SummaryPrompt,

		AnalystFeeds:  feedLines(s.AnalystFeeds, false),
		AnalystPrompt: s.AnalystPrompt,
		AnalystModel:  s.AnalystModel,

		NotificationTrigger:    s.NotificationTrigger,
		NotificationWebhookURL: s.NotificationWebhookURL,
	}
}

const generatedHeader = `# GENERATED FILE - DO NOT EDIT
# Managed by matcha-webapp; regenerated from settings before every run.
# Any changes made here will be overwritten.
# Edit your configuration at http://<host>:7321/settings
`

// GenerateConfig renders Settings as matcha's config.yaml.
//
// This goes through yaml.Marshal rather than string concatenation on purpose:
// summary_prompt and analyst_prompt are free text that will contain colons,
// quotes and newlines, and hand-quoting them is how you end up emitting a file
// matcha cannot parse.
func GenerateConfig(s Settings) ([]byte, error) {
	body, err := yaml.Marshal(toMatchaConfig(s))
	if err != nil {
		return nil, fmt.Errorf("marshal matcha config: %w", err)
	}
	header := generatedHeader + "# Generated: " + time.Now().UTC().Format(time.RFC3339) + "\n\n"
	return append([]byte(header), body...), nil
}

// WriteConfig generates and atomically writes config.yaml. Mode 0600 because
// the file carries openai_api_key.
func WriteConfig(s Settings, path string) error {
	data, err := GenerateConfig(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// maskSecretsInConfig blanks the API key so the generated config can be shown
// in the browser without painting a live credential into the DOM, where a
// screenshot or a screen share would leak it.
func maskSecretsInConfig(yamlText, key string) string {
	if key == "" {
		return yamlText
	}
	return strings.ReplaceAll(yamlText, key, "********")
}
