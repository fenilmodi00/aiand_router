package proxy

import (
	"context"
	"sort"
	"strings"

	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
)

type requestImageCountContextKey struct{}

func readmitForcedImageLimit(req router.Request, model string) router.Request {
	_, imageLimitExcluded := req.ImageLimitExcludedModels[model]
	if !imageLimitExcluded {
		return req
	}
	req.Exclusions, _ = req.Exclusions.ReadmitForcedImageLimit(model)
	req.ExcludedModels = withoutModels(req.ExcludedModels, []string{model})
	req.ImageCount = 0
	return req
}

func withRequestImageCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, requestImageCountContextKey{}, n)
}

func requestImageCountFromContext(ctx context.Context) int {
	count, _ := ctx.Value(requestImageCountContextKey{}).(int)
	return count
}

func excludeImageLimitModels(
	imageCount int,
	enabledProviders map[string]struct{},
	customBindings map[string][]string,
	excluded, available map[string]struct{},
) (map[string]struct{}, []string) {
	if imageCount <= 0 || len(available) == 0 {
		return excluded, nil
	}

	var out map[string]struct{}
	var removed []string
	for model := range available {
		if _, alreadyExcluded := excluded[model]; alreadyExcluded {
			continue
		}
		if len(customBindings[model]) > 0 {
			continue
		}
		entry, ok := catalog.ByID(model)
		if !ok || entry.ImageInput == catalog.ImageInputUnsupported ||
			catalog.AcceptsImageCount(model, imageCount, enabledProviders) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(excluded)+1)
			for id := range excluded {
				out[id] = struct{}{}
			}
		}
		out[model] = struct{}{}
		removed = append(removed, model)
	}
	if len(removed) == 0 {
		return excluded, nil
	}
	sort.Strings(removed)
	return out, removed
}

// applyImageLimitPrefilter runs the image-limit pre-filter over a request's
// available pool and performs the boilerplate every ingress surface shared:
// the request-scoped log line when something was excluded, and the defensive
// copy of the removed list that the exclusion-reporting request fields
// (Safety/ContextWindow/ImageLimit sets) consume downstream.
func applyImageLimitPrefilter(
	ctx context.Context,
	imageCount int,
	enabledProviders map[string]struct{},
	customBindings map[string][]string,
	excluded, available map[string]struct{},
) (map[string]struct{}, []string) {
	excluded, imageLimitExcluded := excludeImageLimitModels(imageCount, enabledProviders, customBindings, excluded, available)
	if len(imageLimitExcluded) > 0 {
		observability.FromContext(ctx).Info("image limit pre-filter: excluded models whose bindings cap images below the request count",
			"image_count", imageCount,
			"excluded_models", strings.Join(imageLimitExcluded, ","),
		)
	}
	return excluded, append([]string(nil), imageLimitExcluded...)
}
