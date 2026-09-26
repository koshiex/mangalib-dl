// Package mangalib is a small client for the MangaLib API and its image CDN.
package mangalib

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultBaseURL is the MangaLib API root.
	DefaultBaseURL = "https://api.cdnlibs.org/api"
	// SiteReferer must be sent with every request: the API and CDN answer 403 without it.
	SiteReferer = "https://mangalib.me/"
	// SiteURL is the public title page prefix.
	SiteURL = "https://mangalib.me/ru/manga/"

	siteID    = 1
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

	// defaultAPIInterval keeps the client under the API limit of 100 requests per minute.
	defaultAPIInterval = 650 * time.Millisecond
	defaultMaxRetries  = 4
	defaultBackoffBase = time.Second
	defaultTimeout     = 2 * time.Minute
	maxBackoff         = 30 * time.Second

	maxAPIResponseBytes = 32 << 20
	maxImageBytes       = 64 << 20
)

// serverPriority is the order in which image servers are tried.
// "main" and "secondary" serve originals but are often geo-blocked; "download" and "compress" serve JPEG.
var serverPriority = []string{"main", "secondary", "download", "compress"}

var mangaFields = []string{"summary", "authors", "artists", "genres", "otherNames"}

var (
	// ErrNotFound is returned when the API has no such title or chapter.
	ErrNotFound = errors.New("not found")
	errTooLarge = errors.New("response too large")
)

// APIError is a non-successful API response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("mangalib api: HTTP %d: %s", e.Status, e.Message)
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		msg += " (title may require authorization, set MANGALIB_TOKEN)"
	}
	return msg
}

// Is makes errors.Is(err, ErrNotFound) work for 404 responses.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

func newAPIError(status int, body []byte) *APIError {
	var payload struct {
		Message string `json:"message"`
		Data    struct {
			Toast struct {
				Message string `json:"message"`
			} `json:"toast"`
		} `json:"data"`
	}
	var msg string
	if json.Unmarshal(body, &payload) == nil {
		msg = cmp.Or(payload.Data.Toast.Message, payload.Message)
	}
	return &APIError{Status: status, Message: cmp.Or(msg, http.StatusText(status))}
}

// Client talks to the MangaLib API. It is safe for concurrent use.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	token       string
	maxRetries  int
	backoffBase time.Duration
	limiter     *limiter

	serversMu sync.Mutex
	servers   []string
	preferred atomic.Int32
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithToken sets a bearer token for titles that require authorization.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithAPIInterval sets the minimal delay between API requests.
func WithAPIInterval(d time.Duration) Option {
	return func(c *Client) { c.limiter = &limiter{interval: d} }
}

// WithMaxRetries sets how many times a failed request is retried.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// WithBackoffBase sets the first retry delay; it doubles on each attempt.
func WithBackoffBase(d time.Duration) Option {
	return func(c *Client) { c.backoffBase = d }
}

// NewClient creates a Client with production defaults.
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient:  &http.Client{Timeout: defaultTimeout},
		baseURL:     DefaultBaseURL,
		maxRetries:  defaultMaxRetries,
		backoffBase: defaultBackoffBase,
		limiter:     &limiter{interval: defaultAPIInterval},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Search finds titles by name. Pages start at 1.
func (c *Client) Search(ctx context.Context, query string, page int) (SearchPage, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Add("site_id[]", strconv.Itoa(siteID))
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	env, err := getJSON[[]MangaSummary](ctx, c, "/manga", q)
	if err != nil {
		return SearchPage{}, err
	}
	return SearchPage{Items: env.Data, HasNext: env.Meta.HasNextPage}, nil
}

// Manga returns title details by slug, e.g. "254222--i-hoi-cha-gwang-jeon-sa".
func (c *Client) Manga(ctx context.Context, slug string) (Manga, error) {
	q := url.Values{}
	for _, f := range mangaFields {
		q.Add("fields[]", f)
	}
	env, err := getJSON[Manga](ctx, c, mangaPath(slug), q)
	if err != nil {
		return Manga{}, err
	}
	if env.Data.ID == 0 {
		return Manga{}, fmt.Errorf("manga %q: %w", slug, ErrNotFound)
	}
	return env.Data, nil
}

// Chapters returns the chapter list in reading order.
func (c *Client) Chapters(ctx context.Context, slug string) ([]Chapter, error) {
	env, err := getJSON[[]Chapter](ctx, c, mangaPath(slug)+"/chapters", nil)
	return env.Data, err
}

// ChapterPages returns pages of one chapter. Branch 0 selects the default branch.
func (c *Client) ChapterPages(ctx context.Context, slug, volume, number string, branch int64) (ChapterPages, error) {
	q := url.Values{}
	q.Set("number", number)
	q.Set("volume", volume)
	if branch > 0 {
		q.Set("branch_id", strconv.FormatInt(branch, 10))
	}
	env, err := getJSON[ChapterPages](ctx, c, mangaPath(slug)+"/chapter", q)
	return env.Data, err
}

// FetchImage downloads a page, trying image servers in order and remembering the first one that works.
func (c *Client) FetchImage(ctx context.Context, pagePath string) ([]byte, error) {
	if isAbsoluteURL(pagePath) {
		return c.FetchImageURL(ctx, pagePath)
	}
	servers, err := c.imageServers(ctx)
	if err != nil {
		return nil, err
	}
	start := int(c.preferred.Load())
	errs := make([]error, 0, len(servers))
	for i := range servers {
		idx := (start + i) % len(servers)
		data, err := c.FetchImageURL(ctx, joinURL(servers[idx], pagePath))
		if err == nil {
			c.preferred.Store(int32(idx))
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("image %s: all servers failed: %w", pagePath, errors.Join(errs...))
}

// FetchImageURL downloads an image from an absolute URL with the headers the CDN expects.
func (c *Client) FetchImageURL(ctx context.Context, rawURL string) ([]byte, error) {
	res, err := c.fetch(ctx, rawURL, kindImage)
	if err != nil {
		return nil, err
	}
	if res.status != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", hostOf(rawURL), res.status)
	}
	if len(res.body) == 0 {
		return nil, fmt.Errorf("%s: empty body", hostOf(rawURL))
	}
	return res.body, nil
}

func (c *Client) imageServers(ctx context.Context) ([]string, error) {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	if len(c.servers) > 0 {
		return c.servers, nil
	}
	q := url.Values{}
	q.Add("fields[]", "imageServers")
	env, err := getJSON[struct {
		ImageServers []ImageServer `json:"imageServers"`
	}](ctx, c, "/constants", q)
	if err != nil {
		return nil, err
	}
	servers := orderServers(env.Data.ImageServers, siteID)
	if len(servers) == 0 {
		return nil, errors.New("mangalib api: no image servers for site")
	}
	c.servers = servers
	return servers, nil
}

// orderServers keeps servers of the site, sorted by serverPriority, without duplicate URLs.
func orderServers(all []ImageServer, site int) []string {
	seen := make(map[string]bool)
	var out []string
	for _, id := range serverPriority {
		for _, s := range all {
			u := strings.TrimRight(s.URL, "/")
			if s.ID != id || u == "" || seen[u] || !slices.Contains(s.SiteIDs, site) {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

type envelope[T any] struct {
	Data T `json:"data"`
	Meta struct {
		HasNextPage bool `json:"has_next_page"`
	} `json:"meta"`
}

func getJSON[T any](ctx context.Context, c *Client, path string, query url.Values) (envelope[T], error) {
	var env envelope[T]
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	res, err := c.fetch(ctx, u, kindAPI)
	if err != nil {
		return env, fmt.Errorf("GET %s: %w", path, err)
	}
	if res.status != http.StatusOK {
		return env, fmt.Errorf("GET %s: %w", path, newAPIError(res.status, res.body))
	}
	if err := json.Unmarshal(res.body, &env); err != nil {
		return env, fmt.Errorf("GET %s: decode response: %w", path, err)
	}
	return env, nil
}

type requestKind int

const (
	kindAPI requestKind = iota
	kindImage
)

type response struct {
	status int
	header http.Header
	body   []byte
}

func (c *Client) fetch(ctx context.Context, rawURL string, kind requestKind) (response, error) {
	for attempt := 0; ; attempt++ {
		if kind == kindAPI {
			if err := c.limiter.wait(ctx); err != nil {
				return response{}, err
			}
		}
		res, err := c.fetchOnce(ctx, rawURL, kind)
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		if !isRetryable(res, err) || attempt >= c.maxRetries {
			return res, err
		}
		if err := sleepContext(ctx, c.backoff(attempt, res.header)); err != nil {
			return response{}, err
		}
	}
}

func (c *Client) fetchOnce(ctx context.Context, rawURL string, kind requestKind) (response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", SiteReferer)
	limit := int64(maxImageBytes)
	if kind == kindAPI {
		limit = maxAPIResponseBytes
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Site-Id", strconv.Itoa(siteID))
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	} else {
		req.Header.Set("Accept", "image/jpeg,image/png,image/*;q=0.8")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return response{}, err
	}
	if int64(len(body)) > limit {
		return response{}, fmt.Errorf("%w: more than %d bytes", errTooLarge, limit)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

func isRetryable(res response, err error) bool {
	if err != nil {
		return !errors.Is(err, errTooLarge)
	}
	return res.status == http.StatusTooManyRequests || res.status >= http.StatusInternalServerError
}

func (c *Client) backoff(attempt int, header http.Header) time.Duration {
	if s := header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxBackoff)
		}
	}
	return min(c.backoffBase<<attempt, maxBackoff)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter enforces a minimal interval between calls.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (l *limiter) wait(ctx context.Context) error {
	if l.interval <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()
	return sleepContext(ctx, time.Until(at))
}

func mangaPath(slug string) string {
	return "/manga/" + url.PathEscape(slug)
}

func joinURL(server, path string) string {
	return strings.TrimRight(server, "/") + "/" + strings.TrimLeft(path, "/")
}

func isAbsoluteURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "image server"
}
