// Package geocode reverse-geocodes coordinates with OpenStreetMap Nominatim.
// Usage policy: max 1 request/second and an identifying User-Agent, which is
// plenty for office check-ins.
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
}

func New(userAgent string) *Client {
	return &Client{
		BaseURL:   "https://nominatim.openstreetmap.org",
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 6 * time.Second},
	}
}

// Reverse returns a human-readable address. Callers should treat an error as
// non-fatal: the coordinates are still recorded.
func (c *Client) Reverse(ctx context.Context, lat, lng float64) (string, error) {
	q := url.Values{
		"format": {"jsonv2"}, "lat": {fmt.Sprintf("%.6f", lat)}, "lon": {fmt.Sprintf("%.6f", lng)},
		"zoom": {"18"}, "accept-language": {"id"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/reverse?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nominatim: HTTP %d", resp.StatusCode)
	}
	var out struct {
		DisplayName string `json:"display_name"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("nominatim: %s", out.Error)
	}
	return out.DisplayName, nil
}
