package main

import (
	"strings"
	"testing"
)

func hasFieldError(res ValidationResult, field string) bool {
	for _, e := range res.Errors {
		if e.Field == field {
			return true
		}
	}
	return false
}

// A feed URL containing whitespace makes matcha treat the tail as an item
// count and log.Fatalf when it is not a number, killing the whole run.
func TestValidateRejectsFeedURLWithSpace(t *testing.T) {
	res := ValidateSettings(Settings{
		Schedule: defaultSchedule,
		Feeds:    []Feed{{URL: "https://example.com/a feed.xml", Enabled: true}},
	})
	if res.OK() {
		t.Fatal("expected a validation error for a URL containing a space")
	}
	if !hasFieldError(res, "feeds[0]") {
		t.Errorf("error not attributed to feeds[0]: %v", res.Errors)
	}
}

func TestValidateFeedSchemes(t *testing.T) {
	bad := []string{"ftp://example.com/feed", "file:///etc/passwd", "example.com/feed", "javascript:alert(1)"}
	for _, u := range bad {
		res := ValidateSettings(Settings{
			Schedule: defaultSchedule,
			Feeds:    []Feed{{URL: u, Enabled: true}},
		})
		if res.OK() {
			t.Errorf("expected %q to be rejected", u)
		}
	}
	res := ValidateSettings(Settings{
		Schedule: defaultSchedule,
		Feeds:    []Feed{{URL: "https://example.com/feed", Enabled: true}},
	})
	if !res.OK() {
		t.Errorf("valid feed rejected: %v", res.Errors)
	}
}

func TestValidateKeywordCommas(t *testing.T) {
	// Keywords are joined with commas into one matcha value, so an embedded
	// comma would silently become two keywords.
	res := ValidateSettings(Settings{
		Schedule:           defaultSchedule,
		GoogleNewsKeywords: []string{"one,two"},
	})
	if res.OK() {
		t.Fatal("expected a comma in a keyword to be rejected")
	}
}

func TestValidateOPMLPathConfinement(t *testing.T) {
	old := configDir
	configDir = "/app/config"
	defer func() { configDir = old }()

	for _, p := range []string{"/etc/passwd", "relative/path.opml", "/app/config/../../etc/shadow"} {
		res := ValidateSettings(Settings{Schedule: defaultSchedule, OPMLFilePath: p})
		if res.OK() {
			t.Errorf("expected %q to be rejected", p)
		}
	}
	res := ValidateSettings(Settings{Schedule: defaultSchedule, OPMLFilePath: "/app/config/subs.opml"})
	if !res.OK() {
		t.Errorf("valid OPML path rejected: %v", res.Errors)
	}
}

func TestValidateLimitRange(t *testing.T) {
	res := ValidateSettings(Settings{
		Schedule: defaultSchedule,
		Feeds:    []Feed{{URL: "https://example.com/feed", Limit: 5000, Enabled: true}},
	})
	if res.OK() {
		t.Error("expected an out-of-range item limit to be rejected")
	}
}

func TestValidateWarnings(t *testing.T) {
	res := ValidateSettings(Settings{
		Schedule:      defaultSchedule,
		SunriseSunset: true,
		WeatherLat:    55.68,
		WeatherLon:    0,
	})
	if !res.OK() {
		t.Fatalf("this should be valid, just warned about: %v", res.Errors)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning about sunrise/sunset needing both coordinates")
	}
}

func TestNormalizeDropsEmptyFeedsAndStripsLimits(t *testing.T) {
	s := Settings{
		Schedule:           "  0 6 * * *  ",
		Feeds:              []Feed{{URL: "  https://a.example/f  ", Enabled: true}, {URL: "   "}},
		AnalystFeeds:       []Feed{{URL: "https://b.example/f", Limit: 40, Enabled: true}},
		GoogleNewsKeywords: []string{" go ", "", "rust"},
	}
	s.normalize()

	if len(s.Feeds) != 1 || s.Feeds[0].URL != "https://a.example/f" {
		t.Errorf("feeds = %+v", s.Feeds)
	}
	// Analyst feeds have no configurable limit upstream.
	if s.AnalystFeeds[0].Limit != 0 {
		t.Errorf("analyst feed limit should be cleared, got %d", s.AnalystFeeds[0].Limit)
	}
	if len(s.GoogleNewsKeywords) != 2 || s.GoogleNewsKeywords[0] != "go" {
		t.Errorf("keywords = %v", s.GoogleNewsKeywords)
	}
	if s.Schedule != "0 6 * * *" {
		t.Errorf("schedule = %q", s.Schedule)
	}
}

// Get must deep-copy: the UI reorders feeds, and an aliased backing array
// would let a caller mutate the store in place.
func TestGetReturnsDeepCopy(t *testing.T) {
	st := &settingsStore{}
	st.s = Settings{Feeds: []Feed{{URL: "https://a.example/f", Enabled: true}}}

	got := st.Get()
	got.Feeds[0].URL = "https://evil.example/f"

	if st.s.Feeds[0].URL != "https://a.example/f" {
		t.Error("mutating the returned Settings changed the store")
	}
}

func TestValidateCronSchedule(t *testing.T) {
	valid := []string{"0 6 * * *", "*/30 * * * *", "0 6,18 * * *", "0 6 * * 1-5", "@daily", "@hourly"}
	for _, s := range valid {
		if err := ValidateCronSchedule(s); err != nil {
			t.Errorf("ValidateCronSchedule(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"", "not a cron", "0 6 * *", "99 6 * * *", "0 6 * * 9",
		"@reboot",              // meaningless for a digest, and unsupported
		"0 6 * * * ; rm -rf /", // too many fields
	}
	for _, s := range invalid {
		if err := ValidateCronSchedule(s); err == nil {
			t.Errorf("ValidateCronSchedule(%q) = nil, want an error", s)
		}
	}
}

func TestCronErrorMessageIsUsable(t *testing.T) {
	err := ValidateCronSchedule("0 6 * *")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "5 fields") {
		t.Errorf("error message should explain the field count, got %q", err)
	}
}
