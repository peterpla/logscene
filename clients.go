// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// clients.go defines the three interfaces that abstract all outbound HTTP calls,
// plus their production HTTP implementations.
//
// Having these as interfaces means tests can inject fakes without any network
// access, and a future cloud deployment can swap in different backends.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

// ---------------------------------------------------------------------------
// TimezoneClient — resolves lat/lon to an IANA timezone name
// ---------------------------------------------------------------------------

// TimezoneClient looks up the IANA timezone name for a geographic coordinate.
type TimezoneClient interface {
	GetTimezone(ctx context.Context, lat, lng float64) (string, error)
}

// tzdbResponse is the relevant subset of the timezonedb.com JSON response.
type tzdbResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	ZoneName string `json:"zoneName"`
}

// HTTPTimezoneClient calls timezonedb.com.
type HTTPTimezoneClient struct {
	apiKey string
	client *http.Client
}

// NewHTTPTimezoneClient creates a production TimezoneClient.
func NewHTTPTimezoneClient(apiKey string) *HTTPTimezoneClient {
	return &HTTPTimezoneClient{
		apiKey: apiKey,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// GetTimezone fetches the IANA timezone name for the given coordinates.
// It retries automatically on HTTP 429 (rate-limited to 1 req/s by timezonedb.com).
func (c *HTTPTimezoneClient) GetTimezone(ctx context.Context, lat, lng float64) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("timezonedb API key not configured (set TIMELAPSE_TZDB)")
	}

	q := url.Values{}
	q.Set("key", c.apiKey)
	q.Set("format", "json")
	q.Set("by", "position")
	q.Set("lat", fmt.Sprintf("%.7f", lat))
	q.Set("lng", fmt.Sprintf("%.7f", lng))
	rawURL := "http://api.timezonedb.com/v2.1/get-time-zone?" + q.Encode()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", fmt.Errorf("GetTimezone: build request: %w", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("GetTimezone: do request: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			delay := time.Duration(1+rand.Intn(4)) * time.Second
			log.Printf("GetTimezone: rate limited (429), retrying in %v", delay)
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("GetTimezone: read body: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GetTimezone: unexpected status %s", resp.Status)
		}

		var result tzdbResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return "", fmt.Errorf("GetTimezone: decode response: %w", err)
		}
		if result.Status != "OK" {
			return "", fmt.Errorf("GetTimezone: API error: %s", result.Message)
		}

		return result.ZoneName, nil
	}
}

// ---------------------------------------------------------------------------
// SolarClient — fetches sunrise, solar noon, and sunset times
// ---------------------------------------------------------------------------

// SolarTimes holds the three solar event times for a single day, all in UTC.
type SolarTimes struct {
	Sunrise   time.Time
	SolarNoon time.Time
	Sunset    time.Time
}

// SolarClient fetches solar times for a location on a given date.
// The date embedded in the time.Time parameter is used; its timezone determines
// which calendar date is requested — pass a time already converted to the
// webcam's timezone so the correct local date is used.
type SolarClient interface {
	GetSolarTimes(ctx context.Context, lat, lng float64, webcamDate time.Time) (SolarTimes, error)
}

const solarTimeLayout = "2006-01-02T15:04:05Z"

// solarAPIResponse is the wrapper returned by sunrise-sunset.org.
type solarAPIResponse struct {
	Results struct {
		Sunrise   string `json:"sunrise"`
		SolarNoon string `json:"solar_noon"`
		Sunset    string `json:"sunset"`
	} `json:"results"`
	Status string `json:"status"`
}

// HTTPSolarClient calls sunrise-sunset.org.
type HTTPSolarClient struct {
	client *http.Client
}

// NewHTTPSolarClient creates a production SolarClient.
func NewHTTPSolarClient() *HTTPSolarClient {
	return &HTTPSolarClient{client: &http.Client{Timeout: 5 * time.Second}}
}

// GetSolarTimes fetches solar event times for the given location and date.
// webcamDate must be in the webcam's local timezone so the correct calendar
// date is extracted (guards against the server running in a different timezone).
func (c *HTTPSolarClient) GetSolarTimes(ctx context.Context, lat, lng float64, webcamDate time.Time) (SolarTimes, error) {
	year, month, day := webcamDate.Date()

	q := url.Values{}
	q.Set("lat", fmt.Sprintf("%.7f", lat))
	q.Set("lng", fmt.Sprintf("%.7f", lng))
	q.Set("date", fmt.Sprintf("%04d-%02d-%02d", year, int(month), day))
	q.Set("formatted", "0")
	rawURL := "https://api.sunrise-sunset.org/json?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: build request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: unexpected status %s", resp.Status)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: read body: %w", err)
	}
	// sunrise-sunset.org sometimes returns "+00:00" instead of "Z"
	raw = bytes.ReplaceAll(raw, []byte(`+00:00"`), []byte(`Z"`))

	var result solarAPIResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: decode response: %w", err)
	}
	if result.Status != "OK" {
		return SolarTimes{}, fmt.Errorf("GetSolarTimes: API status %q", result.Status)
	}

	parse := func(s string) (time.Time, error) {
		t, err := time.Parse(solarTimeLayout, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("GetSolarTimes: parse %q: %w", s, err)
		}
		return t, nil
	}

	sunrise, err := parse(result.Results.Sunrise)
	if err != nil {
		return SolarTimes{}, err
	}
	noon, err := parse(result.Results.SolarNoon)
	if err != nil {
		return SolarTimes{}, err
	}
	sunset, err := parse(result.Results.Sunset)
	if err != nil {
		return SolarTimes{}, err
	}

	return SolarTimes{Sunrise: sunrise, SolarNoon: noon, Sunset: sunset}, nil
}

// ---------------------------------------------------------------------------
// ImageFetcher — retrieves a webcam image over HTTP
// ---------------------------------------------------------------------------

// ImageFetcher fetches a single image from a webcam URL.
type ImageFetcher interface {
	Fetch(ctx context.Context, rawURL string) (body io.ReadCloser, contentType string, err error)
}

// HTTPImageFetcher is the production ImageFetcher.
type HTTPImageFetcher struct {
	client *http.Client
}

// NewHTTPImageFetcher creates a production ImageFetcher.
func NewHTTPImageFetcher() *HTTPImageFetcher {
	return &HTTPImageFetcher{client: &http.Client{Timeout: 10 * time.Second}}
}

// Fetch performs an HTTP GET and returns the response body and Content-Type.
// The caller is responsible for closing the returned body.
func (f *HTTPImageFetcher) Fetch(ctx context.Context, rawURL string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("Fetch: build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Fetch: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("Fetch %s: unexpected status %s", rawURL, resp.Status)
	}

	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// extensionForContentType maps a MIME type to a file extension.
func extensionForContentType(ct string) string {
	// Strip any parameters (e.g., "image/jpeg; charset=utf-8")
	for i, c := range ct {
		if c == ';' {
			ct = ct[:i]
			break
		}
	}
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}
