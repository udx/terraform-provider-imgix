package imgix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"
)

// Source is a decoded Imgix source. Pointer fields mirror the API: nil means
// the API did not return the field (not that it is false/empty).
type Source struct {
	ID               string
	Name             string
	Enabled          *bool
	DateDeployed     *int64
	DeploymentStatus string
	SecureURLToken   *string
	Deployment       *Deployment
}

// Deployment is the deployment object of a source. Only GCS origins are
// modeled. gcs_secret_key is never returned on GET and therefore absent here.
type Deployment struct {
	Type                  string            `json:"type,omitempty"`
	ImgixSubdomains       []string          `json:"imgix_subdomains,omitempty"`
	GCSBucket             string            `json:"gcs_bucket,omitempty"`
	GCSAccessKey          string            `json:"gcs_access_key,omitempty"`
	GCSPrefix             *string           `json:"gcs_prefix,omitempty"`
	CustomDomains         []string          `json:"custom_domains,omitempty"`
	DefaultParams         map[string]string `json:"default_params,omitempty"`
	SecureURLEnabled      *bool             `json:"secure_url_enabled,omitempty"`
	CacheTTLBehavior      string            `json:"cache_ttl_behavior,omitempty"`
	CacheTTLValue         *int64            `json:"cache_ttl_value,omitempty"`
	CacheTTLError         *int64            `json:"cache_ttl_error,omitempty"`
	CrossdomainXMLEnabled *bool             `json:"crossdomain_xml_enabled,omitempty"`
	ImageError            *string           `json:"image_error,omitempty"`
	ImageMissing          *string           `json:"image_missing,omitempty"`
	ImageErrorAppendQS    *bool             `json:"image_error_append_qs,omitempty"`
	ImageMissingAppendQS  *bool             `json:"image_missing_append_qs,omitempty"`
	Annotation            *string           `json:"annotation,omitempty"`
	AllowsUpload          *bool             `json:"allows_upload,omitempty"`
}

// sourceData is the JSON:API data member for a single source.
type sourceData struct {
	ID         string `json:"id"`
	Attributes struct {
		Name             string      `json:"name"`
		Enabled          *bool       `json:"enabled"`
		DateDeployed     *int64      `json:"date_deployed"`
		DeploymentStatus string      `json:"deployment_status"`
		SecureURLToken   *string     `json:"secure_url_token"`
		Deployment       *Deployment `json:"deployment"`
	} `json:"attributes"`
}

func decodeSource(raw json.RawMessage) (*Source, error) {
	var d sourceData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decoding source: %w", err)
	}
	return &Source{
		ID:               d.ID,
		Name:             d.Attributes.Name,
		Enabled:          d.Attributes.Enabled,
		DateDeployed:     d.Attributes.DateDeployed,
		DeploymentStatus: d.Attributes.DeploymentStatus,
		SecureURLToken:   d.Attributes.SecureURLToken,
		Deployment:       d.Attributes.Deployment,
	}, nil
}

// GetSource fetches one source by ID.
func (c *Client) GetSource(ctx context.Context, id string) (*Source, error) {
	raw, err := c.do(ctx, "GET", "/sources/"+url.PathEscape(id), nil, nil, true)
	if err != nil {
		return nil, err
	}
	return decodeSource(raw)
}

// GetSourceAttributes fetches one source and returns its raw attributes map,
// including fields the typed model does not cover. Used to verify that
// unmodeled fields survive updates.
func (c *Client) GetSourceAttributes(ctx context.Context, id string) (map[string]interface{}, error) {
	raw, err := c.do(ctx, "GET", "/sources/"+url.PathEscape(id), nil, nil, true)
	if err != nil {
		return nil, err
	}
	var d struct {
		Attributes map[string]interface{} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decoding source: %w", err)
	}
	return d.Attributes, nil
}

// ListSources lists all sources, optionally filtered by JSON:API filter
// fields such as "deployment.imgix_subdomains" or "name".
func (c *Client) ListSources(ctx context.Context, filters map[string]string) ([]*Source, error) {
	query := url.Values{}
	for k, v := range filters {
		query.Set("filter["+k+"]", v)
	}
	rawList, err := c.list(ctx, "/sources", query)
	if err != nil {
		return nil, err
	}
	out := make([]*Source, 0, len(rawList))
	for _, raw := range rawList {
		s, err := decodeSource(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// FindSourceBySubdomains returns the source whose deployment.imgix_subdomains
// exactly matches subs (order-insensitive), or nil if none exists.
func (c *Client) FindSourceBySubdomains(ctx context.Context, subs []string) (*Source, error) {
	if len(subs) == 0 {
		return nil, nil
	}
	candidates, err := c.ListSources(ctx, map[string]string{"deployment.imgix_subdomains": subs[0]})
	if err != nil {
		return nil, err
	}
	for _, s := range candidates {
		if s.Deployment != nil && equalStringSets(s.Deployment.ImgixSubdomains, subs) {
			return s, nil
		}
	}
	return nil, nil
}

// CreateSource creates (and deploys) a source. attrs is encoded verbatim
// under data.attributes. To avoid duplicates after ambiguous failures
// (transport error or 5xx after the server committed), a failed create is
// recovered by looking up a source with the same imgix_subdomains.
func (c *Client) CreateSource(ctx context.Context, attrs map[string]interface{}) (*Source, error) {
	raw, err := c.do(ctx, "POST", "/sources", nil, map[string]interface{}{
		"data": map[string]interface{}{"type": "sources", "attributes": attrs},
	}, false)
	if err == nil {
		return decodeSource(raw)
	}

	var apiErr *Error
	isHTTP4xx := errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 429
	if isHTTP4xx {
		return nil, err // validation/conflict errors are final
	}

	// Ambiguous failure: the source may exist. Recover by subdomain set.
	if dep, ok := attrs["deployment"].(map[string]interface{}); ok {
		if rawSubs, ok := dep["imgix_subdomains"].([]string); ok {
			if found, findErr := c.FindSourceBySubdomains(ctx, rawSubs); findErr == nil && found != nil {
				return found, nil
			}
		}
	}
	return nil, err
}

// UpdateSource applies a sparse PATCH: only the attributes present in attrs
// are sent. Deployment changes go under attrs["deployment"].
func (c *Client) UpdateSource(ctx context.Context, id string, attrs map[string]interface{}) (*Source, error) {
	if len(attrs) == 0 {
		return c.GetSource(ctx, id)
	}
	raw, err := c.do(ctx, "PATCH", "/sources/"+url.PathEscape(id), nil, map[string]interface{}{
		"data": map[string]interface{}{"type": "sources", "id": id, "attributes": attrs},
	}, true)
	if err != nil {
		return nil, err
	}
	return decodeSource(raw)
}

// DeleteSource attempts DELETE /sources/:id. The published Management API
// documents only GET/POST/PATCH for sources, so a 405 (or a 404 that turns
// out to be a route miss, verified by a follow-up GET) maps to
// ErrDeletionUnsupported. A source that is gone after the call is success.
func (c *Client) DeleteSource(ctx context.Context, id string) error {
	_, err := c.do(ctx, "DELETE", "/sources/"+url.PathEscape(id), nil, nil, false)
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == 405 {
		return ErrDeletionUnsupported
	}
	if NotFound(err) {
		if _, getErr := c.GetSource(ctx, id); NotFound(getErr) {
			return nil // already gone
		}
		return ErrDeletionUnsupported // route miss: the source is still there
	}
	return err
}

// WaitForStatus polls a source until it reaches its intended terminal state:
// "disabled" when wantDisabled is true, "deployed" otherwise. It returns an
// error naming the source ID on "deleted", on context cancellation, or when
// the deadline passes. Credentials are never included.
func (c *Client) WaitForStatus(ctx context.Context, id string, wantDisabled bool, interval time.Duration) (*Source, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var lastStatus string
	for {
		src, err := c.GetSource(ctx, id)
		if err != nil && !NotFound(err) {
			// Read-after-write lag and transient errors: keep polling until the
			// caller's context deadline.
			lastStatus = "unreadable"
		} else if src != nil {
			lastStatus = src.DeploymentStatus
			switch {
			case wantDisabled && src.DeploymentStatus == "disabled":
				return src, nil
			case !wantDisabled && src.DeploymentStatus == "deployed":
				return src, nil
			case src.DeploymentStatus == "deleted":
				return nil, fmt.Errorf("source %s was deleted while waiting for deployment", id)
			}
		}

		if err := sleep(ctx, interval); err != nil {
			return nil, fmt.Errorf("timed out waiting for source %s to reach %q (last status %q): %w", id, wantedStatus(wantDisabled), lastStatus, err)
		}
	}
}

func wantedStatus(wantDisabled bool) string {
	if wantDisabled {
		return "disabled"
	}
	return "deployed"
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
