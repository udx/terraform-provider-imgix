package imgix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://api.imgix.com/api/v1"
	jsonAPIType    = "application/vnd.api+json"
	maxAttempts    = 6
	maxListPages   = 100
)

// Client is a minimal Imgix Management API client. It authenticates with a
// Bearer API key and speaks JSON:API.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Error is an Imgix API error decoded from the JSON:API errors envelope.
type Error struct {
	Status    int
	Title     string
	Detail    string
	RequestID string

	retryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("imgix API error: status=%d title=%s detail=%s", e.Status, e.Title, e.Detail)
	}
	return fmt.Sprintf("imgix API error: status=%d title=%s", e.Status, e.Title)
}

// NotFound reports whether err is a confirmed HTTP 404 from the API.
// Auth failures (401/403) and transient errors are never NotFound.
func NotFound(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Status == http.StatusNotFound
	}
	return false
}

// ErrDeletionUnsupported is returned when the API refuses DELETE on sources
// (405) or when a DELETE 404 turns out to be a route miss (the source still
// exists afterwards).
var ErrDeletionUnsupported = errors.New("the Imgix API does not support deleting sources")

// DeletionUnsupported reports whether err is ErrDeletionUnsupported.
func DeletionUnsupported(err error) bool {
	return errors.Is(err, ErrDeletionUnsupported)
}

type errorEnvelope struct {
	Errors []struct {
		Detail string `json:"detail"`
		ID     string `json:"id"`
		Status string `json:"status"`
		Title  string `json:"title"`
	} `json:"errors"`
}

type document struct {
	Data json.RawMessage `json:"data"`
}

type listDocument struct {
	Data  []json.RawMessage `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

// do performs one API call and returns the decoded "data" member. Retryable
// calls are retried with bounded exponential backoff on 429 and 5xx and on
// transport errors; non-retryable calls (creates) are only retried on 429,
// which is rejected before anything is committed server-side.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload interface{}, retryable bool) (json.RawMessage, error) {
	var body []byte
	var err error
	if payload != nil {
		if body, err = json.Marshal(payload); err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt, lastErr)); err != nil {
				return nil, err
			}
		}

		reqURL := c.baseURL + path
		if len(query) > 0 {
			reqURL += "?" + query.Encode()
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Accept", jsonAPIType)
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		if payload != nil {
			req.Header.Set("Content-Type", jsonAPIType)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !retryable {
				return nil, err
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !retryable {
				return nil, readErr
			}
			continue
		}

		if resp.StatusCode >= 400 {
			apiErr := decodeError(resp.StatusCode, resp.Header, respBody)
			lastErr = apiErr
			retryStatus := resp.StatusCode == 429 || resp.StatusCode >= 500
			if !retryStatus || (!retryable && resp.StatusCode != 429) {
				return nil, apiErr
			}
			continue
		}

		if len(respBody) == 0 {
			return nil, nil
		}
		var doc document
		if err := json.Unmarshal(respBody, &doc); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		return doc.Data, nil
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// list follows JSON:API pagination (links.next) and returns every data member.
func (c *Client) list(ctx context.Context, path string, query url.Values) ([]json.RawMessage, error) {
	var out []json.RawMessage
	next := ""
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, fmt.Errorf("listing %s: exceeded %d pages", path, maxListPages)
		}

		var raw []byte
		var doc listDocument

		// links.next is an absolute URL; request it directly, preserving auth.
		reqURL := c.baseURL + path
		if next != "" {
			reqURL = next
		} else if len(query) > 0 {
			reqURL += "?" + query.Encode()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Accept", jsonAPIType)
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		raw, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			return nil, decodeError(resp.StatusCode, resp.Header, raw)
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		out = append(out, doc.Data...)
		if doc.Links.Next == "" {
			return out, nil
		}
		next = doc.Links.Next
	}
}

func decodeError(status int, header http.Header, body []byte) *Error {
	apiErr := &Error{Status: status}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Errors) > 0 {
		apiErr.Title = env.Errors[0].Title
		apiErr.Detail = env.Errors[0].Detail
		apiErr.RequestID = env.Errors[0].ID
		if n, convErr := strconv.Atoi(env.Errors[0].Status); convErr == nil {
			apiErr.Status = n
		}
	}
	if apiErr.Title == "" {
		apiErr.Title = http.StatusText(status)
	}
	if apiErr.RequestID == "" {
		apiErr.RequestID = header.Get("X-Request-Id")
	}
	if status == http.StatusTooManyRequests {
		if ra := header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				d := time.Duration(secs) * time.Second
				if d > 60*time.Second {
					d = 60 * time.Second
				}
				apiErr.retryAfter = d
			}
		}
	}
	return apiErr
}

func backoff(attempt int, lastErr error) time.Duration {
	var apiErr *Error
	if errors.As(lastErr, &apiErr) && apiErr.retryAfter > 0 {
		return apiErr.retryAfter
	}
	d := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d + time.Duration(rand.Intn(250))*time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
