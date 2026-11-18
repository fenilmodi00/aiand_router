package postgres

import (
	"context"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/google/uuid"
)

type clusterModelListRepo struct {
	tx sqlc.DBTX
}

// NewClusterModelListRepo constructs a per-key per-cluster allowlist repo.
func NewClusterModelListRepo(tx sqlc.DBTX) auth.ClusterModelListRepository {
	return &clusterModelListRepo{tx: tx}
}

type userClusterModelListRepo struct {
	tx sqlc.DBTX
}

// NewUserClusterModelListRepo constructs a per-user per-cluster selection repo.
func NewUserClusterModelListRepo(tx sqlc.DBTX) auth.UserClusterModelListRepository {
	return &userClusterModelListRepo{tx: tx}
}

func (r *clusterModelListRepo) GetForAPIKey(ctx context.Context, apiKeyID string) ([]auth.ClusterModelList, error) {
	q := dbbudget.Queries(r.tx)
	return clusterModelListQuery(apiKeyID,
		func(id uuid.UUID) ([]sqlc.RouterClusterModelList, error) {
			return q.GetClusterModelListsByAPIKey(ctx, id)
		},
		toAuthClusterModelList)
}

func (r *userClusterModelListRepo) GetForUser(ctx context.Context, routerUserID string) ([]auth.UserClusterModelList, error) {
	q := dbbudget.Queries(r.tx)
	return clusterModelListQuery(routerUserID,
		func(id uuid.UUID) ([]sqlc.RouterModelRouterUserClusterModelList, error) {
			return q.GetUserClusterModelListsByUser(ctx, id)
		},
		toAuthUserClusterModelList)
}

// clusterModelListQuery parses the owner id, runs the table's allowlist query,
// and projects each row into its auth domain type. Shared by the per-key and
// per-user allowlist repos, which differ only in table and owner column.
func clusterModelListQuery[Row, Out any](
	ownerID string,
	fetch func(uuid.UUID) ([]Row, error),
	project func(Row) Out,
) ([]Out, error) {
	parsed, err := uuid.Parse(ownerID)
	if err != nil {
		return nil, err
	}
	rows, err := fetch(parsed)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, project), nil
}

func toAuthClusterModelList(row sqlc.RouterClusterModelList) auth.ClusterModelList {
	return auth.ClusterModelList{
		APIKeyID:     row.APIKeyID.String(),
		ClusterLabel: row.ClusterLabel,
		Models:       row.Models,
	}
}

func toAuthUserClusterModelList(row sqlc.RouterModelRouterUserClusterModelList) auth.UserClusterModelList {
	return auth.UserClusterModelList{
		RouterUserID: row.RouterUserID.String(),
		ClusterLabel: row.ClusterLabel,
		Models:       row.Models,
	}
}
