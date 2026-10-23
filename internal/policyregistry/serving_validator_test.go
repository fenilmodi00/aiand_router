package policyregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/policyregistry"
)

type staticDestinationEndpoints struct {
	worker     policyregistry.WorkerAttestation
	classifier policyregistry.ClassifierAttestation
	err        error
}

func (e staticDestinationEndpoints) ValidateWorker(context.Context, policyregistry.RevisionBinding, policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	return e.worker, e.err
}
func (e staticDestinationEndpoints) AttestClassifier(context.Context, policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	return e.classifier, e.err
}

func validatedEndpoints(prepared policyregistry.PreparedSelection) staticDestinationEndpoints {
	return staticDestinationEndpoints{
		classifier: policyregistry.ClassifierAttestation{Identity: prepared.Candidate.Classifier.Identity, Package: prepared.Candidate.Classifier.Package, Configuration: prepared.Candidate.Classifier.Configuration, AuxiliaryModels: maps.Clone(prepared.Candidate.Classifier.AuxiliaryModels), Revision: prepared.Binding.Classifier.Name, Ready: true},
		worker:     policyregistry.WorkerAttestation{Identity: policyregistry.WorkerIdentity{Target: prepared.Target, Project: prepared.Binding.Project, Region: prepared.Binding.Region, Revision: prepared.Binding.Router.Name, ImageDigest: prepared.Binding.Router.ImageDigest, Configuration: prepared.Binding.Router.Configuration}, Requirements: prepared.Candidate.Requirements, Selection: prepared.Selection, CatalogArms: prepared.Policy.AllArms(), Ready: true},
	}
}

// countingDestinationEndpoints answers like the real destinations — the classifier attests its own
// revision, the worker derives its attestation from the validation request — and records every call.
type countingDestinationEndpoints struct {
	t               *testing.T
	store           policyregistry.ServingStore
	classifier      policyregistry.ClassifierAttestation
	classifierCalls int
	workerCalls     int
}

func (e *countingDestinationEndpoints) AttestClassifier(_ context.Context, binding policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	e.classifierCalls++
	attestation := e.classifier
	attestation.Revision = binding.Name
	return attestation, nil
}

func (e *countingDestinationEndpoints) ValidateWorker(ctx context.Context, _ policyregistry.RevisionBinding, request policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	e.workerCalls++
	prepared, err := policyregistry.ReadPreparedSelection(ctx, e.store, request.Target, request.ProfileKey, request.Selection)
	require.NoError(e.t, err)
	return validatedEndpoints(prepared).worker, nil
}

func preparedLanes(t *testing.T, store policyregistry.ServingStore, set policyregistry.SelectionSet) []policyregistry.PreparedSelection {
	t.Helper()
	lanes := make([]policyregistry.PreparedSelection, 0, len(set.Profiles)+1)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, "", set.Default)
	require.NoError(t, err)
	lanes = append(lanes, prepared)
	for key, selection := range set.Profiles {
		profile, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, key, selection)
		require.NoError(t, err)
		lanes = append(lanes, profile)
	}
	return lanes
}

// flakyDestinationEndpoints fails the first classifier attestation and would succeed afterwards.
type flakyDestinationEndpoints struct {
	calls int
}

func (e *flakyDestinationEndpoints) AttestClassifier(context.Context, policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	e.calls++
	if e.calls == 1 {
		return policyregistry.ClassifierAttestation{}, errors.New("HTTP 503")
	}
	return policyregistry.ClassifierAttestation{Ready: true}, nil
}

func (e *flakyDestinationEndpoints) ValidateWorker(context.Context, policyregistry.RevisionBinding, policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	return policyregistry.WorkerAttestation{}, errors.New("unexpected worker validation")
}

// destinationValidationEntry returns the per-activation destination validation audit line.
func destinationValidationEntry(t *testing.T, audit *bytes.Buffer) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(audit)
	for {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if _, exists := entry["destination_validation_lanes"]; exists {
			return entry
		}
	}
}
