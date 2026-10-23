package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aiand-os/router/internal/policyclient"
	"aiand-os/router/internal/postgres"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/escalation"
	"aiand-os/router/internal/router/hmm/armid"
	"aiand-os/router/internal/router/hmm/rosterdata"
	"aiand-os/router/internal/router/hmm/selection"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/server"
)

type atomicClassifierConfig struct {
	Release               string      `json:"release"`
	ReleaseSHA256         string      `json:"release_sha256"`
	Endpoint              string      `json:"endpoint"`
	SelectionPolicyFile   string      `json:"selection_policy_file"`
	SelectionPolicySHA256 string      `json:"selection_policy_sha256"`
	InstallationIDs       []uuid.UUID `json:"installation_ids"`
}

// Legacy/isolated composition only. Managed ingress must first gain an attested
// Modal binding; this config must not bypass the controller's release ownership.
func configureAtomicClassifier(service *proxy.Service, pool *pgxpool.Pool, providers map[string]struct{}, deploymentMode server.DeploymentMode) error {
	path := os.Getenv("ROUTER_LLM_CLASSIFIER_CONFIG")
	if path == "" {
		return nil
	}
	if managedServingEnabled(deploymentMode) {
		return errors.New("llm classifier requires managed Modal release integration before enabling managed serving")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config atomicClassifierConfig
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("classifier configuration contains trailing JSON")
	}
	roster, err := rosterdata.Load(config.SelectionPolicyFile)
	if err != nil {
		return err
	}
	escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	if roster.SHA256 != config.SelectionPolicySHA256 || roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 || !slices.Equal(roster.ClassOrder, escalationClasses) {
		return errors.New("classifier selection policy digest, schema or taxonomy mismatch")
	}
	client, err := policyclient.NewAtomicClassifier(config.Endpoint, os.Getenv("ROUTER_LLM_CLASSIFIER_BEARER"), config.Release, config.ReleaseSHA256, nil)
	if err != nil {
		return err
	}
	if err := service.WithClassifierSessions(proxy.ClassifierSessionConfig{Release: config.Release, ReleaseSHA256: config.ReleaseSHA256, SelectionPolicySHA256: config.SelectionPolicySHA256, SigningKey: []byte(os.Getenv("ROUTER_LLM_CLASSIFIER_SIGNING_KEY")), InstallationIDs: config.InstallationIDs}, postgres.NewClassifierSessionRepo(pool), client); err != nil {
		return err
	}
	capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, HonorsPreferredModels: true, HonorsQualityPriceBias: true, HonorsClusterModelLists: true, AuthoritativePerTurnSelection: true}
	resolver := policy.NewResolver(catalog.HMMRoutingTargetSet(providers), providers, armid.ForModel)
	classifierRouter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyLLMClassifier, Unavailable: router.ErrClassifierUnavailable,
		ClassifierArtifactID: config.Release, ClassifierArtifactSHA256: config.ReleaseSHA256,
		SelectionPolicyReleaseID: config.Release, SelectionPolicySHA256: config.SelectionPolicySHA256},
		policy.AtomicClassifierFacts{Release: config.Release, ReleaseSHA256: config.ReleaseSHA256}, resolver).
		WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
	service.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: classifierRouter, Unavailable: router.ErrClassifierUnavailable, Capabilities: capabilities})
	return nil
}
