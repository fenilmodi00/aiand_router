// Selector tests run against the real catalog: the bundle no longer carries
// a price or throughput table, so pricing comes from catalog.PrimaryPriceFor
// and the cheapest/fastest selectors agree by construction.

func TestCheapestModel(t *testing.T) {
	registry := &ModelRegistry{
		DeployedModels: []DeployedEntry{
			{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND, BenchColumn: "col-a"},
			{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND, BenchColumn: "col-b"},
			{Model: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND, BenchColumn: "col-c"},
		},
	}
	available := map[string]struct{}{providers.ProviderAIAND: {}}

	t.Run("picks the cheapest catalog-priced candidate", func(t *testing.T) {
		p, m, ok := CheapestModel(registry, available)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", m)
	})

	t.Run("returns false when no provider matches", func(t *testing.T) {
		_, _, ok := CheapestModel(registry, map[string]struct{}{"other": {}})
		assert.False(t, ok)
	})

	t.Run("skips entries the catalog does not price", func(t *testing.T) {
		unpriced := &ModelRegistry{
			DeployedModels: []DeployedEntry{
				{Model: "not-in-catalog", Provider: providers.ProviderAIAND, BenchColumn: "col-x"},
				{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND, BenchColumn: "col-b"},
			},
		}
		p, m, ok := CheapestModel(unpriced, available)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", m)
	})
}

func TestCheapestModelInSet(t *testing.T) {
	registry := &ModelRegistry{
		DeployedModels: []DeployedEntry{
			{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND, BenchColumn: "col-a"},
			{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND, BenchColumn: "col-b"},
			{Model: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND, BenchColumn: "col-c"},
		},
	}
	available := map[string]struct{}{providers.ProviderAIAND: {}}

	t.Run("respects allowSet — cheapest within allow", func(t *testing.T) {
		allow := map[string]struct{}{"z-ai/glm-5.3": {}, "moonshotai/kimi-k3": {}}
		p, m, ok := CheapestModelInSet(registry, available, nil, allow)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "z-ai/glm-5.3", m, "kimi-k3 is cheaper but excluded by allowSet")
	})

	t.Run("ok=false when allowSet has no available model", func(t *testing.T) {
		allow := map[string]struct{}{"nope": {}}
		_, _, ok := CheapestModelInSet(registry, available, nil, allow)
		assert.False(t, ok)
	})

	t.Run("nil allowSet behaves like CheapestModel", func(t *testing.T) {
		p, m, ok := CheapestModelInSet(registry, available, nil, nil)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", m)
	})

	t.Run("denySet excludes models even when allowed by allowSet", func(t *testing.T) {
		// Guards PR #100: tier clamping bypassed the request's denylist.
		allow := map[string]struct{}{"z-ai/glm-5.3": {}, "moonshotai/kimi-k3": {}}
		deny := map[string]struct{}{"z-ai/glm-5.3": {}}
		p, m, ok := CheapestModelInSet(registry, available, deny, allow)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "moonshotai/kimi-k3", m, "glm-5.3 is cheaper but denylisted")
	})

	t.Run("denySet emptying pool yields ok=false", func(t *testing.T) {
		deny := map[string]struct{}{"z-ai/glm-5.3": {}, "deepseek/deepseek-v4.1-flash": {}, "moonshotai/kimi-k3": {}}
		_, _, ok := CheapestModelInSet(registry, available, deny, nil)
		assert.False(t, ok)
	})
}

// FastestModel has no throughput annotations left to read, so it must agree
// with the cheapest catalog-priced candidate.
func TestFastestModel(t *testing.T) {
	registry := &ModelRegistry{
		DeployedModels: []DeployedEntry{
			{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND, BenchColumn: "col-a"},
			{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND, BenchColumn: "col-b"},
		},
	}

	t.Run("matches the cheapest candidate", func(t *testing.T) {
		available := map[string]struct{}{providers.ProviderAIAND: {}}
		p, m, ok := FastestModel(registry, available)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", m)
	})

	t.Run("ok=false when no provider matches", func(t *testing.T) {
		_, _, ok := FastestModel(registry, map[string]struct{}{"other-provider": {}})
		assert.False(t, ok)
	})
}

func TestFastestModelInSet(t *testing.T) {
	registry := &ModelRegistry{
		DeployedModels: []DeployedEntry{
			{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND, BenchColumn: "col-vf"},
			{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND, BenchColumn: "col-a"},
			{Model: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND, BenchColumn: "col-c"},
		},
	}
	available := map[string]struct{}{providers.ProviderAIAND: {}}

	t.Run("low-tier clamp picks the cheapest in-ceiling model", func(t *testing.T) {
		allow := map[string]struct{}{"deepseek/deepseek-v4.1-flash": {}, "z-ai/glm-5.3": {}}
		p, m, ok := FastestModelInSet(registry, available, nil, allow)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", m)
	})

	t.Run("denySet excludes the pick, falls to the next cheapest", func(t *testing.T) {
		allow := map[string]struct{}{"deepseek/deepseek-v4.1-flash": {}, "z-ai/glm-5.3": {}}
		deny := map[string]struct{}{"deepseek/deepseek-v4.1-flash": {}}
		p, m, ok := FastestModelInSet(registry, available, deny, allow)
		require.True(t, ok)
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, "z-ai/glm-5.3", m, "v4.1-flash is cheaper but denylisted")
	})

	t.Run("ok=false when allowSet has no available model", func(t *testing.T) {
		allow := map[string]struct{}{"nope": {}}
		_, _, ok := FastestModelInSet(registry, available, nil, allow)
		assert.False(t, ok)
	})
}

// The latest bundle's low-tier clamp must still resolve a real model.
func TestFastestModel_RealLatestBundle_LowTierResolves(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)
	available := make(map[string]struct{}, len(bundle.Registry.DeployedModels))
	for _, e := range bundle.Registry.DeployedModels {
		available[e.Provider] = struct{}{}
	}
	allow := catalog.AllowedAtOrBelow(catalog.TierLow)

	fastP, fastM, ok := FastestModelInSet(bundle.Registry, available, nil, allow)
	require.True(t, ok)
	_, cheapM, ok := CheapestModelInSet(bundle.Registry, available, nil, allow)
	require.True(t, ok)

	assert.Equal(t, "aiand", fastP)
	assert.NotEmpty(t, fastM, "fastest low-tier model")
	assert.Equal(t, cheapM, fastM, "with no throughput table the fastest pick is the cheapest")
}

// Custom bindings resolve a deployed model to its aliased provider, but they
// must not widen the candidate roster: only the bundle's roster is pickable.
func TestFastestModelForRequest_CustomBindingsDoNotWidenRoster(t *testing.T) {
	const aliased = "z-ai/glm-5.3-flash"
	const unaliased = "z-ai/glm-5.2"
	registry := &ModelRegistry{
		DeployedModels: []DeployedEntry{
			{Model: aliased, Provider: providers.ProviderAIAND},
			{Model: unaliased, Provider: providers.ProviderAIAND},
		},
	}
	available := map[string]struct{}{providers.ProviderAIAND: {}}

	t.Run("resolves the model the key aliases", func(t *testing.T) {
		p, m, ok := FastestModelForRequest(registry, available, nil, nil, RequestBindings{
			Custom: map[string][]string{aliased: {providers.ProviderAIAND}},
		})
		require.True(t, ok, "an aliased deployed model must be hard-pinnable")
		assert.Equal(t, providers.ProviderAIAND, p)
		assert.Equal(t, aliased, m)
	})

	t.Run("custom bindings must not widen the roster for direct-vendor keys", func(t *testing.T) {
		_, m, ok := FastestModelForRequest(registry,
			map[string]struct{}{providers.ProviderAIAND: {}}, nil, nil, RequestBindings{
				Custom: map[string][]string{"z-ai/glm-5.2": {providers.ProviderAIAND}},
			})
		require.True(t, ok)
		assert.Equal(t, aliased, m, "custom bindings must not widen the roster for direct-vendor keys")
	})

	t.Run("excluded_models still applies", func(t *testing.T) {
		_, m, ok := FastestModelForRequest(registry, available,
			map[string]struct{}{aliased: {}}, nil, RequestBindings{
				Custom: map[string][]string{aliased: {providers.ProviderAIAND}},
			})
		require.True(t, ok, "the remaining roster model is still pickable")
		assert.Equal(t, unaliased, m, "the excluded model must not be picked")
	})
}
