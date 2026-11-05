package breaker

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	overloadedModel = "claude-opus-5"
	otherModel      = "gpt-5.6-luna"
	orgA            = "org-a"
	orgB            = "org-b"
	orgC            = "org-c"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *clock { return &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }

func anyModel(string) bool { return true }

// stateOf reports the position of model's shared circuit, or of org's
// isolated circuit for it when isolated is set.
func stateOf(r *Registry, org, model string, isolated bool) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.circuits[circuitKey(org, model, isolated)]
	switch {
	case c == nil:
		return StateClosed
	case c.state == StateOpen && c.probe.Load() != nil && r.now().Before(c.probe.Load().until):
		return StateHalfOpen
	}
	return c.state
}

func withdrawnFrom(r *Registry, org, model string) bool {
	_, ok := r.Withdrawn(org)[model]
	return ok
}

func newRegistry(c *clock) *Registry {
	return New(Config{
		Strikes:          3,
		Window:           2 * time.Minute,
		OrgConfirmations: 2,
		RecoveryBase:     30 * time.Second,
		RecoveryCap:      2 * time.Minute,
	}, c.Now)
}

func strike(r *Registry, org, model string, isolated bool, times int) Transition {
	var last Transition
	for range times {
		last = r.RecordOverload(org, model, isolated)
	}
	return last
}

// openShared opens the shared circuit for overloadedModel with orgA's
// failures alone, which also confirms it for orgA.
func openShared(t *testing.T, r *Registry) {
	t.Helper()
	strike(r, orgA, overloadedModel, false, 3)
	require.Equal(t, StateOpen, stateOf(r, orgA, overloadedModel, false))
}

func claim(t *testing.T, r *Registry, org string) Probe {
	t.Helper()
	probe, _, ok := r.ClaimProbe(org, anyModel)
	require.True(t, ok)
	return probe
}

func TestSharedCircuitOpensOnStrikesFromAnyOrganization(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	r.RecordOverload(orgA, overloadedModel, false)
	r.RecordOverload(orgB, overloadedModel, false)
	assert.Equal(t, StateClosed, stateOf(r, orgA, overloadedModel, false))

	opened := r.RecordOverload(orgC, overloadedModel, false)

	assert.Equal(t, StateOpen, opened.To)
	assert.Equal(t, 30*time.Second, opened.RecoveryAfter)
}

// Opening the shared circuit for org A does not withdraw the model from org B
// until org B has itself observed enough overloads.
func TestOpenSharedCircuitWithdrawsOnlyFromConfirmedOrganizations(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)

	assert.True(t, withdrawnFrom(r, orgA, overloadedModel))
	assert.False(t, withdrawnFrom(r, orgB, overloadedModel))

	first := r.RecordOverload(orgB, overloadedModel, false)
	assert.False(t, first.Confirmed)
	assert.False(t, withdrawnFrom(r, orgB, overloadedModel), "one failure is below the confirmation threshold")

	second := r.RecordOverload(orgB, overloadedModel, false)
	assert.True(t, second.Confirmed)
	assert.True(t, withdrawnFrom(r, orgB, overloadedModel))
}

// An organization's failures from just before the circuit opened count toward
// its confirmation, so it does not have to fail again after the open.
func TestFailuresBeforeTheOpenCountTowardConfirmation(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	strike(r, orgB, overloadedModel, false, 2)
	c.Advance(30 * time.Second)
	r.RecordOverload(orgA, overloadedModel, false)

	assert.True(t, withdrawnFrom(r, orgB, overloadedModel))
	assert.False(t, withdrawnFrom(r, orgA, overloadedModel), "a single failure does not confirm")
}

func TestOrgFailuresOlderThanTheEpisodeDoNotConfirm(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	r.RecordOverload(orgB, overloadedModel, false)
	c.Advance(3 * time.Minute)
	openShared(t, r)
	r.RecordOverload(orgB, overloadedModel, false)

	assert.False(t, withdrawnFrom(r, orgB, overloadedModel))
}

func TestStrikesOutsideWindowAgeOut(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	strike(r, orgA, overloadedModel, false, 2)
	c.Advance(2*time.Minute + time.Second)
	r.RecordOverload(orgA, overloadedModel, false)

	assert.Equal(t, StateClosed, stateOf(r, orgA, overloadedModel, false))
	assert.Nil(t, r.Withdrawn(orgA))
}

// A gateway organization's circuit is its own: its failures never move the
// shared circuit, and no other organization sees it.
func TestIsolatedCircuitStaysWithItsOrganization(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	opened := strike(r, orgC, overloadedModel, true, 3)

	assert.Equal(t, StateOpen, opened.To)
	assert.True(t, opened.Isolated)
	assert.True(t, withdrawnFrom(r, orgC, overloadedModel), "the owner needs no further confirmation")
	assert.Equal(t, StateClosed, stateOf(r, orgC, overloadedModel, false), "the shared circuit is untouched")
	assert.False(t, withdrawnFrom(r, orgA, overloadedModel))
	assert.Equal(t, StateClosed, stateOf(r, orgA, overloadedModel, true))
}

// Shared evidence does not reach a gateway organization's own circuit either.
func TestSharedCircuitDoesNotTouchIsolatedCircuits(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)

	assert.Equal(t, StateClosed, stateOf(r, orgC, overloadedModel, true))
	assert.False(t, withdrawnFrom(r, orgC, overloadedModel))
}

func TestOpenCircuitIsNotDueBeforeRecoveryTimeout(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)

	c.Advance(29 * time.Second)
	_, _, ok := r.ClaimProbe(orgA, anyModel)

	assert.False(t, ok)
}

func TestHalfOpenAdmitsExactlyOneProbe(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	for _, org := range []string{orgA, orgB, orgA, orgB, orgA, orgB, orgA, orgB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok := r.ClaimProbe(org, anyModel); ok {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, claims)
	assert.Equal(t, StateHalfOpen, stateOf(r, orgA, overloadedModel, false))
	assert.True(t, withdrawnFrom(r, orgA, overloadedModel), "half-open stays withdrawn for everyone but the prober")
}

// A served probe closes the shared circuit for every organization and clears
// their confirmations: the next episode starts from scratch.
func TestServedProbeClosesForEveryOrganization(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	strike(r, orgB, overloadedModel, false, 2)
	c.Advance(30 * time.Second)

	closed := r.ProbeSucceeded(claim(t, r, orgB))

	assert.Equal(t, StateClosed, closed.To)
	assert.Nil(t, r.Withdrawn(orgA))
	assert.Nil(t, r.Withdrawn(orgB))
	strike(r, orgA, overloadedModel, false, 2)
	assert.Equal(t, StateClosed, stateOf(r, orgA, overloadedModel, false), "strikes from before the outage do not carry over")
}

// An organization that never confirmed may still probe, so a shared circuit
// recovers even if only unaffected organizations are sending traffic.
func TestUnconfirmedOrganizationMayProbeSharedCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)

	probe := claim(t, r, orgB)

	assert.Equal(t, overloadedModel, probe.Model)
}

func TestOnlyTheOwnerProbesAnIsolatedCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	strike(r, orgC, overloadedModel, true, 3)
	c.Advance(30 * time.Second)

	_, _, claimedByOther := r.ClaimProbe(orgA, anyModel)
	owner, _, ok := r.ClaimProbe(orgC, anyModel)

	assert.False(t, claimedByOther)
	require.True(t, ok)
	assert.Equal(t, StateClosed, r.ProbeSucceeded(owner).To)
	assert.Nil(t, r.Withdrawn(orgC))
}

func TestFailedProbeReopensWithBackoffUpToCap(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)

	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 2 * time.Minute} {
		c.Advance(5 * time.Minute)

		reopened := r.ProbeFailed(claim(t, r, orgA))

		assert.Equal(t, StateOpen, reopened.To)
		assert.Equal(t, want, reopened.RecoveryAfter)
		assert.True(t, withdrawnFrom(r, orgA, overloadedModel), "confirmations carry over a failed probe")
	}
	c.Advance(2*time.Minute - time.Second)
	_, _, ok := r.ClaimProbe(orgA, anyModel)
	assert.False(t, ok, "the capped recovery timeout still applies")
}

// Only the probe decides recovery: a 529 from a request without the probe
// neither reopens the circuit early nor counts against the probe.
func TestOverloadWhileHalfOpenLeavesProbeInCharge(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)
	probe := claim(t, r, orgA)

	r.RecordOverload(orgA, overloadedModel, false)

	assert.Equal(t, StateHalfOpen, stateOf(r, orgA, overloadedModel, false))
	assert.Equal(t, StateClosed, r.ProbeSucceeded(probe).To)
}

func TestReleasedProbeIsDueAgain(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)

	released := r.ReleaseProbe(claim(t, r, orgA))
	again := claim(t, r, orgA)

	assert.Equal(t, StateOpen, released.To)
	assert.Equal(t, overloadedModel, again.Model)
}

// A request whose probe was released does not keep one model first in line:
// the next claim goes to the due circuit not yet offered.
func TestReleasedProbeYieldsToUnofferedCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(time.Second)
	strike(r, orgA, otherModel, false, 3)
	c.Advance(30 * time.Second)

	first := claim(t, r, orgA)
	require.Equal(t, overloadedModel, first.Model, "the longest-open circuit is offered first")
	r.ReleaseProbe(first)
	second := claim(t, r, orgA)

	assert.Equal(t, otherModel, second.Model)
}

func TestStaleProbeCannotResolveCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)
	stale := claim(t, r, orgA)
	r.ProbeFailed(stale)
	c.Advance(time.Minute)
	current := claim(t, r, orgA)

	assert.False(t, r.ProbeSucceeded(stale).Changed(), "a resolved probe cannot close the circuit")
	assert.False(t, r.ReleaseProbe(stale).Changed())
	assert.Equal(t, StateHalfOpen, stateOf(r, orgA, overloadedModel, false))
	assert.Equal(t, StateClosed, r.ProbeSucceeded(current).To)
}

func TestAbandonedProbeIsReclaimedAfterLease(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)
	stale := claim(t, r, orgA)

	c.Advance(probeLease - time.Second)
	_, _, early := r.ClaimProbe(orgA, anyModel)
	c.Advance(time.Second)
	_, _, late := r.ClaimProbe(orgA, anyModel)

	assert.False(t, early)
	assert.True(t, late)
	assert.False(t, r.ProbeSucceeded(stale).Changed(), "the abandoned holder cannot resolve the new probe")
}

func TestClaimSkipsIneligibleModels(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)

	_, _, ok := r.ClaimProbe(orgA, func(string) bool { return false })

	assert.False(t, ok)
	assert.Equal(t, StateOpen, stateOf(r, orgA, overloadedModel, false), "an ineligible request leaves the circuit due for the next one")
}

// An organization with withdrawals of its own still never sees another
// organization's gateway circuit.
func TestIsolatedCircuitDoesNotLeakIntoOtherOrganizationsWithdrawals(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	strike(r, orgA, otherModel, false, 3)
	require.True(t, withdrawnFrom(r, orgA, otherModel))

	strike(r, orgC, overloadedModel, true, 3)

	assert.Equal(t, map[string]struct{}{otherModel: {}}, r.Withdrawn(orgA))
	assert.Equal(t, map[string]struct{}{overloadedModel: {}}, r.Withdrawn(orgC))
}

// Claiming and releasing a probe run on every eligible request while a
// circuit is due, so they must not wait on the registry lock.
func TestClaimAndReleaseDoNotTakeTheLock(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openShared(t, r)
	c.Advance(30 * time.Second)
	r.mu.Lock()
	defer r.mu.Unlock()

	done := make(chan bool)
	go func() {
		probe, _, ok := r.ClaimProbe(orgA, anyModel)
		released := r.ReleaseProbe(probe).Changed()
		withdrawn := r.Withdrawn(orgA) != nil
		done <- ok && released && withdrawn
	}()

	select {
	case completed := <-done:
		assert.True(t, completed)
	case <-time.After(5 * time.Second):
		t.Fatal("ClaimProbe or ReleaseProbe blocked on the registry lock")
	}
}

// One-off strikes from many organizations' gateways are dropped once they
// age out, so a long-lived replica does not accumulate circuits.
func TestAgedOutClosedCircuitsAreSwept(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	r.RecordOverload(orgC, overloadedModel, true)
	r.RecordOverload(orgA, otherModel, true)
	c.Advance(time.Minute)
	r.RecordOverload(orgA, otherModel, true)
	c.Advance(time.Minute + time.Second)

	r.RecordOverload(orgB, otherModel, true)

	_, staleKept := r.circuits[circuitKey(orgC, overloadedModel, true)]
	_, liveKept := r.circuits[circuitKey(orgA, otherModel, true)]
	assert.False(t, staleKept, "a closed circuit whose strikes all aged out is dropped")
	assert.True(t, liveKept, "a closed circuit with a strike inside the window is kept")
}
