package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxFeedBytes       = 2 << 20 // 2 MiB is far more than a title list needs
	maxPreviewItems    = 10
	maxPreviewTitleLen = 200
	maxFeedRedirects   = 3
	feedRequestTimeout = 10 * time.Second
)

// feedClient is deliberately strict about time: a preview is an interactive
// smoke test, so a slow feed should fail visibly rather than hang the UI.
var feedClient = &http.Client{
	Timeout: feedRequestTimeout,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxFeedRedirects {
			return fmt.Errorf("stopped after %d redirects", maxFeedRedirects)
		}
		// Re-check on every hop: a redirect to file:// or similar must not
		// sneak past the check done on the original URL.
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
		}
		return nil
	},
}

// PreviewItem is one entry from the feed.
type PreviewItem struct {
	Title string `json:"title"`
	Link  string `json:"link,omitempty"`
}

// PreviewResult is the response body for /api/feed-preview.
type PreviewResult struct {
	OK        bool          `json:"ok"`
	FeedTitle string        `json:"feed_title,omitempty"`
	Items     []PreviewItem `json:"items,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// charsetReader passes through the encodings that cover essentially all real
// feeds and rejects the rest.
//
// Proper transcoding would mean pulling in golang.org/x/net/html/charset and
// its dependency tree. For a list of titles, treating latin-1 as bytes may
// mangle the odd accented character; that is not worth five extra modules.
// Please do not "fix" this by adding the dependency.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii", "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported character encoding %q", label)
	}
}

// localName strips any namespace prefix so RSS 1.0/RDF and namespaced Atom
// documents parse the same way as plain RSS 2.0.
func localName(n xml.Name) string {
	name := n.Local
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len([]rune(s)) > maxPreviewTitleLen {
		s = string([]rune(s)[:maxPreviewTitleLen]) + "…"
	}
	return s
}

// parseFeedTitles walks the document collecting the feed title and the first
// maxPreviewItems item titles. It stops as soon as it has enough, so a large
// feed does not have to be parsed in full.
func parseFeedTitles(r io.Reader) (feedTitle string, items []PreviewItem, err error) {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.CharsetReader = charsetReader
	d.Entity = xml.HTMLEntity

	var (
		inItem  bool
		current PreviewItem
	)

	for {
		tok, tokErr := d.Token()
		if tokErr == io.EOF {
			break
		}
		if tokErr != nil {
			// Return what we have: a body cut off at the size limit is still
			// useful, and this is a smoke test rather than a strict parser.
			return feedTitle, items, tokErr
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch name := localName(t.Name); name {
			case "item", "entry":
				inItem = true
				current = PreviewItem{}

			case "title":
				var text string
				if err := d.DecodeElement(&text, &t); err != nil {
					continue
				}
				if inItem {
					if current.Title == "" {
						current.Title = cleanTitle(text)
					}
				} else if feedTitle == "" {
					feedTitle = cleanTitle(text)
				}

			case "link":
				if !inItem {
					d.Skip()
					continue
				}
				// Atom puts the URL in an attribute; RSS puts it in the body.
				var href, rel string
				for _, a := range t.Attr {
					switch strings.ToLower(a.Name.Local) {
					case "href":
						href = a.Value
					case "rel":
						rel = strings.ToLower(a.Value)
					}
				}
				if href != "" {
					if current.Link == "" && (rel == "" || rel == "alternate") {
						current.Link = strings.TrimSpace(href)
					}
					d.Skip()
					continue
				}
				var text string
				if err := d.DecodeElement(&text, &t); err != nil {
					continue
				}
				if current.Link == "" {
					current.Link = strings.TrimSpace(text)
				}

			default:
				// Descend normally; nothing else is interesting.
			}

		case xml.EndElement:
			switch localName(t.Name) {
			case "item", "entry":
				if inItem {
					if current.Title == "" {
						current.Title = "(untitled)"
					}
					items = append(items, current)
					inItem = false
					current = PreviewItem{}
				}
				if len(items) >= maxPreviewItems {
					return feedTitle, items, nil
				}
			}
		}
	}
	return feedTitle, items, nil
}

// previewFeed fetches rawURL and extracts a handful of item titles.
//
// This makes the server fetch a URL chosen by the caller, so it is an SSRF
// surface. It sits behind authentication, which makes it equivalent to giving
// the admin a curl. Private and loopback addresses are deliberately NOT
// blocked: previewing a feed from a LAN aggregator (Miniflux, FreshRSS,
// TT-RSS) is a normal thing to do with a self-hosted tool, and blocking it
// would break a real use case to defend against someone who already has admin.
func previewFeed(ctx context.Context, rawURL string) PreviewResult {
	// Accept a pasted "<url> <limit>" line as well as a bare URL.
	rawURL = parseFeedEntry(rawURL).URL

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return PreviewResult{Error: "That does not look like a valid URL."}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return PreviewResult{Error: "The URL must start with http:// or https://"}
	}
	if u.Host == "" {
		return PreviewResult{Error: "The URL is missing a hostname."}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return PreviewResult{Error: "Could not build a request for that URL."}
	}
	req.Header.Set("User-Agent", "matcha-webapp/1.0 (+feed preview)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")

	resp, err := feedClient.Do(req)
	if err != nil {
		return PreviewResult{Error: describeFetchError(err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PreviewResult{Error: fmt.Sprintf(
			"The server returned %s.", strings.TrimSpace(resp.Status))}
	}

	limited := &countingReader{r: io.LimitReader(resp.Body, maxFeedBytes)}
	feedTitle, items, parseErr := parseFeedTitles(limited)
	truncated := limited.n >= maxFeedBytes

	if len(items) == 0 {
		if parseErr != nil && !truncated {
			return PreviewResult{Error: "That URL did not return a readable RSS or Atom feed."}
		}
		return PreviewResult{Error: "No feed items found at that URL."}
	}

	return PreviewResult{
		OK:        true,
		FeedTitle: feedTitle,
		Items:     items,
		Truncated: truncated,
	}
}

// describeFetchError turns transport errors into something a user can act on.
func describeFetchError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "Could not find that host. Check the address for typos."
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "The feed took too long to respond."
	}
	if errors.Is(err, context.Canceled) {
		return "The request was cancelled."
	}
	if strings.Contains(err.Error(), "redirect") {
		return "That URL redirected too many times."
	}
	if strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "tls") {
		return "The site's HTTPS certificate could not be verified."
	}
	return "Could not reach that URL."
}

// countingReader tracks how much was read so we can tell a feed that ended
// from one that hit the size cap.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type feedPreviewRequest struct {
	URL string `json:"url"`
}

func handleFeedPreview(w http.ResponseWriter, r *http.Request) {
	var req feedPreviewRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read request")
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeJSON(w, http.StatusOK, PreviewResult{Error: "Enter a feed URL first."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), feedRequestTimeout+2*time.Second)
	defer cancel()

	writeJSON(w, http.StatusOK, previewFeed(ctx, req.URL))
}
