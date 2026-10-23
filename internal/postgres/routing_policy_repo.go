package postgres

import (
	"context"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"
)

type routingPolicyRepo struct{ tx sqlc.DBTX }

// NewRoutingPolicyRepo constructs read-only generic installation policy access.
func NewRoutingPolicyRepo(tx sqlc.DBTX) auth.RoutingPolicyRepository {
	return &routingPolicyRepo{tx: tx}
}

func (repo *routingPolicyRepo) GetPolicy(ctx context.Context, installationID string) (auth.RoutingPolicy, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return auth.RoutingPolicy{}, err
	}
	row, err := dbbudget.Queries(repo.tx).GetInstallationRoutingPolicy(ctx, parsedInstallationID)
	if err != nil {
		return auth.RoutingPolicy{}, err
	}
	return auth.RoutingPolicy{Mode: auth.RoutingPolicyMode(row.Mode), Revision: row.Revision}, nil
}

func (repo *routingPolicyRepo) HasAssignment(ctx context.Context, installationID, routerUserID string, revision int64) (bool, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return false, err
	}
	parsedUserID, err := parseUUID(routerUserID)
	if err != nil {
		return false, err
	}
	return dbbudget.Queries(repo.tx).HasInstallationRoutingAssignment(ctx, sqlc.HasInstallationRoutingAssignmentParams{
		InstallationID: parsedInstallationID, RouterUserID: parsedUserID, Revision: revision,
	})
}
