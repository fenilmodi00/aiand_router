package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// ListModels returns the model IDs the endpoint publishes, sorted and
// deduplicated.
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
	listURL, err := modelListURLs(baseURL)
	if err != nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	ids, _, err := c.listModelsAt(ctx, listURL[0])
	return ids, err
}

// modelListURLs returns the catalog URL for baseURL.
func modelListURLs(baseURL string) ([]string, error) {
	primary, err := url.JoinPath(baseURL, "models")
	if err != nil {
		return nil, err
	}
	return []string{primary}, nil
}

// listModelsAt reads the model-list URL. A gateway that demands an entity on
// the catalog GET is retried once with an empty JSON body.
func (c *Client) listModelsAt(ctx context.Context, listURL string) ([]string, int, error) {
	ids, status, err := c.getModelList(ctx, listURL, false)
	if !providers.ModelListNeedsEntity(status) {
		return ids, status, err
	}
	return c.getModelList(ctx, listURL, true)
}

func (c *Client) getModelList(ctx context.Context, listURL string, withEntity bool) ([]string, int, error) {
	var entity io.Reader
	if withEntity {
		entity = bytes.NewReader(providers.EmptyJSONEntity)
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, entity)
	if err != nil {
		return nil, 0, providers.ErrModelDiscoveryDestination
	}
	if withEntity {
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
	}
	c.setAuth(ctx, upstream)
	requestcontext.ApplyWIFTokenType(ctx, upstream)
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
