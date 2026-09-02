package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const rssSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example Feed</title>
    <link>https://example.com</link>
    <item>
      <title>First &amp; foremost</title>
      <link>https://example.com/1</link>
    </item>
    <item>
      <title><![CDATA[Second item]]></title>
      <link>https://example.com/2</link>
    </item>
  </channel>
</rss>`

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <entry>
    <title>Atom One</title>
    <link rel="alternate" href="https://example.com/a1"/>
  </entry>
  <entry>
    <title>Atom Two</title>
    <link href="https://example.com/a2"/>
  </entry>
</feed>`

func TestParseFeedTitlesRSS(t *testing.T) {
	title, items, err := parseFeedTitles(strings.NewReader(rssSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if title != "Example Feed" {
		t.Errorf("feed title = %q", title)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Title != "First & foremost" {
		t.Errorf("items[0].Title = %q (entity not decoded?)", items[0].Title)
	}
	if items[0].Link != "https://example.com/1" {
		t.Errorf("items[0].Link = %q", items[0].Link)
	}
	if items[1].Title != "Second item" {
		t.Errorf("items[1].Title = %q (CDATA not handled?)", items[1].Title)
	}
}

func TestParseFeedTitlesAtom(t *testing.T) {
	title, items, err := parseFeedTitles(strings.NewReader(atomSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if title != "Atom Example" {
		t.Errorf("feed title = %q", title)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	// Atom carries the URL in an attribute, not the element body.
	if items[0].Link != "https://example.com/a1" || items[1].Link != "https://example.com/a2" {
		t.Errorf("atom links not extracted: %+v", items)
	}
}

func TestParseFeedTitlesStopsAtLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss><channel><title>Big</title>`)
	for i := 0; i < 50; i++ {
		b.WriteString(`<item><title>Item</title></item>`)
	}
	b.WriteString(`</channel></rss>`)

	_, items, err := parseFeedTitles(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != maxPreviewItems {
		t.Errorf("items = %d, want %d", len(items), maxPreviewItems)
	}
}

func TestPreviewFeedRejectsNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/f", "gopher://x"} {
		res := previewFeed(context.Background(), u)
		if res.OK {
			t.Errorf("%q should be rejected", u)
		}
	}
}

func TestPreviewFeedHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssSample))
	}))
	defer srv.Close()

	res := previewFeed(context.Background(), srv.URL)
	if !res.OK {
		t.Fatalf("preview failed: %s", res.Error)
	}
	if res.FeedTitle != "Example Feed" || len(res.Items) != 2 {
		t.Errorf("unexpected result: %+v", res)
	}
}

// A pasted "<url> <limit>" line should preview the URL, not fail on it.
func TestPreviewFeedStripsLimitSuffix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(rssSample))
	}))
	defer srv.Close()

	res := previewFeed(context.Background(), srv.URL+" 10")
	if !res.OK {
		t.Fatalf("preview with a limit suffix failed: %s", res.Error)
	}
}

func TestPreviewFeedReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	res := previewFeed(context.Background(), srv.URL)
	if res.OK {
		t.Fatal("expected a failure for a 404")
	}
	if !strings.Contains(res.Error, "404") {
		t.Errorf("error should mention the status, got %q", res.Error)
	}
}

func TestPreviewFeedNonFeedContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>not a feed</body></html>"))
	}))
	defer srv.Close()

	res := previewFeed(context.Background(), srv.URL)
	if res.OK {
		t.Fatal("expected a failure for a non-feed page")
	}
	if res.Error == "" {
		t.Error("expected an explanatory error message")
	}
}

func TestPreviewFeedCapsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	res := previewFeed(context.Background(), srv.URL)
	if res.OK {
		t.Fatal("expected a redirect loop to fail")
	}
}

func TestCharsetReaderRejectsUnknown(t *testing.T) {
	if _, err := charsetReader("utf-8", strings.NewReader("")); err != nil {
		t.Errorf("utf-8 should be accepted: %v", err)
	}
	if _, err := charsetReader("iso-8859-1", strings.NewReader("")); err != nil {
		t.Errorf("iso-8859-1 should be accepted: %v", err)
	}
	if _, err := charsetReader("shift_jis", strings.NewReader("")); err == nil {
		t.Error("an unsupported encoding should be rejected rather than mangled")
	}
}
