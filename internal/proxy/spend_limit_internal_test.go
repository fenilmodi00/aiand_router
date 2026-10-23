package proxy

import (
	"context"
	"errors"
	"testing"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/billing"
	"aiand-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spendLimitRepo is the minimum billing.Repo surface needed to drive
// checkUserMonthlySpendLimit through every branch.
type spendLimitRepo struct {
	spent int64
	limit *int64
	err   error
}

func (r *spendLimitRepo) GetBalance(context.Context, string) (int64, error) { return 0, nil }
func (r *spendLimitRepo) HasActiveOverride(context.Context, string) (bool, error) {
	return false, nil
}
func (r *spendLimitRepo) DebitInference(context.Context, billing.DebitParams) (int64, error) {
	return 0, nil
}
func (r *spendLimitRepo) GetAPIKeySpend(context.Context, string) (int64, *int64, bool, error) {
	return 0, nil, false, nil
}
func (r *spendLimitRepo) GetUserMonthlySpendAndLimit(context.Context, string, string) (int64, *int64, error) {
	return r.spent, r.limit, r.err
}
func (r *spendLimitRepo) GetOrgMonthlySpendAndLimit(context.Context, string) (int64, *int64, error) {
	return 0, nil, nil
}
func (r *spendLimitRepo) GetAutopayConfig(context.Context, billing.Owner) (bool, int64, error) {
	return false, 0, nil
}
func (r *spendLimitRepo) BillingTablesExist(context.Context) (bool, error) { return true, nil }

func spendLimitCtx(userID, orgID string) context.Context {
	ctx := context.Background()
	if userID != "" {
		ctx = context.WithValue(ctx, auth.UserIDContextKey{}, userID)
	}
	if orgID != "" {
		ctx = context.WithValue(ctx, ExternalIDContextKey{}, orgID)
	}
	return ctx
}

func micros(v int64) *int64 { return &v }

// spendLimitCheck drives checkUserMonthlySpendLimit with no header/route (the
// non-subscription paths never consult them), returning just the error.
func spendLimitCheck(s *Service, ctx context.Context) error {
	_, err := s.checkUserMonthlySpendLimit(ctx)
	return err
}


func TestCheckUserMonthlySpendLimit_NoBillingSkips(t *testing.T) {
	s := &Service{}
	assert.NoError(t, spendLimitCheck(s, spendLimitCtx("u1", "org-1")))
}

func TestCheckUserMonthlySpendLimit_NoIdentitySkips(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 10, limit: micros(1), err: errors.New("must not be called")})}
	assert.NoError(t, spendLimitCheck(s, spendLimitCtx("", "org-1")),
		"a request with no resolvable user identity passes through")
}

func TestCheckUserMonthlySpendLimit_NoLimitPasses(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 999_999_999, limit: nil})}
	assert.NoError(t, spendLimitCheck(s, spendLimitCtx("u1", "org-1")))
}

func TestCheckUserMonthlySpendLimit_UnderLimitPasses(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 999_999, limit: micros(1_000_000)})}
	assert.NoError(t, spendLimitCheck(s, spendLimitCtx("u1", "org-1")))
}

func TestCheckUserMonthlySpendLimit_AtLimitRejected(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 1_000_000, limit: micros(1_000_000)})}
	err := spendLimitCheck(s, spendLimitCtx("u1", "org-1"))
	require.Error(t, err)
	assert.ErrorIs(t, err, billing.ErrUserMonthlySpendLimitReached)
}

func TestCheckUserMonthlySpendLimit_ReadErrorFailsClosed(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{err: errors.New("pg down")})}
	err := spendLimitCheck(s, spendLimitCtx("u1", "org-1"))
	require.Error(t, err)
	assert.ErrorIs(t, err, billing.ErrSpendLimitCheckUnavailable)
}

func TestCheckUserMonthlySpendLimit_OverrideSkips(t *testing.T) {
	// Even over the limit (and with a repo that would error), a billing-override
	// request bypasses engineer enforcement — WithBalanceCheck already let it in.
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 1_000_000, limit: micros(1_000_000), err: errors.New("must not be called")})}
	ctx := context.WithValue(spendLimitCtx("u1", "org-1"), billing.HasOverrideContextKey, true)
	assert.NoError(t, spendLimitCheck(s, ctx))
}






func TestCheckUserMonthlySpendLimit_ExemptsSubscriberAllowanceCoveredTurn(t *testing.T) {
	// A turn admitted against an individual Max/Boost allowance debits 0 and
	// settles against that allowance, so it never adds to the engineer's paid
	// spend and a reached cap must not 402 it.
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 1_000_000, limit: micros(1_000_000)})}
	ctx := entitlement.WithCoverage(spendLimitCtx("u1", "org-1"), entitlement.Coverage{
		SubscriberID:       "cs_max",
		EntitlementVersion: 1,
		Plan:               entitlement.PlanMax,
	})
	_, err := s.checkUserMonthlySpendLimit(ctx)
	assert.NoError(t, err, "an allowance-covered turn is not gated on the engineer's paid spend limit")
}

func TestCheckUserMonthlySpendLimit_AppliesToSubscriberPaidFallback(t *testing.T) {
	s := &Service{billing: billing.NewService(&spendLimitRepo{spent: 1_000_000, limit: micros(1_000_000)})}
	ctx := entitlement.WithProductScope(spendLimitCtx("u1", "org-1"), entitlement.PlanMax)

	_, err := s.checkUserMonthlySpendLimit(ctx)

	require.Error(t, err)
	assert.ErrorIs(t, err, billing.ErrUserMonthlySpendLimitReached)
}

