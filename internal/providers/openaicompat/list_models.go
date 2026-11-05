package openaicompat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/requestcontext"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// ListModels returns the model IDs the endpoint publishes, sorted and
// deduplicated, walking modelListURLs until one answers something other than a
// 404.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := c.effectiveBaseURL(ctx)
	if baseURL == "" {
		return nil, providers.ErrModelDiscoveryDestination
	}
	normalizedBaseURL, err := auth.NormalizeBaseURL(&baseURL)
	if err != nil || normalizedBaseURL == nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	baseURL = *normalizedBaseURL
	listURLs, err := modelListURLs(baseURL)
	if err != nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	var (
		ids     []string
		listErr error
	)
	for _, listURL := range listURLs {
		var status int
		ids, status, listErr = c.listModelsAt(ctx, listURL)
		if status != http.StatusNotFound {
			return ids, listErr
		}
	}
	return ids, listErr
}

// modelListURLs returns catalog URLs to try for baseURL, likeliest first.
// When baseURL ends in "/v1", also includes the URL one segment up, for
// gateways that mount their catalog above the versioned API root.
func modelListURLs(baseURL string) ([]string, error) {
	primary, err := url.JoinPath(baseURL, "models")
	if err != nil {
		return nil, err
	}
	urls := []string{primary}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(parsedBaseURL.Path, "/v1") {
		parsedBaseURL.Path = strings.TrimSuffix(parsedBaseURL.Path, "/v1")
		parsedBaseURL.RawPath = ""
		fallback, joinErr := url.JoinPath(parsedBaseURL.String(), "models")
		if joinErr != nil {
			return nil, joinErr
		}
		urls = append(urls, fallback)
	}
	return urls, nil
}

// listModelsAt reads one model-list URL, also reporting the upstream status so
// the caller can decide whether another path is worth trying.
func (c *Client) listModelsAt(ctx context.Context, listURL string) ([]string, int, error) {
	return c.getModelList(ctx, listURL)
}

func (c *Client) getModelList(ctx context.Context, listURL string) ([]string, int, error) {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, 0, providers.ErrModelDiscoveryDestination
	}
	c.setAuth(ctx, upstream)
	requestcontext.ApplyIdentityHeader(ctx, upstream)

	resp, err := c.modelHTTP.Do(upstream)
	if err != nil {
		if errors.Is(err, providers.ErrModelDiscoveryDestination) {
			return nil, 0, providers.ErrModelDiscoveryDestination
		}
		return nil, 0, providers.ErrModelDiscoveryTransport
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, providers.NewModelListStatusError(resp.StatusCode)
	}
	if err != nil {
		return nil, resp.StatusCode, providers.ErrModelDiscoveryTransport
	}
	ids, err := providers.ParseModelIDs(body)
	return ids, resp.StatusCode, err
}

var _ providers.ModelLister = (*Client)(nil)
