package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedModelTwoProviders creates one ai_service (model) with two ai_service_route
// rows pointing at two distinct providers, mirroring deepseek-v4-pro on aihubmix
// (priority 10) + dmxapi (priority 5, provider_model_id "<key>-guan"). Returns
// (serviceID, hiProviderID, loProviderID).
func seedModelTwoProviders(t *testing.T, db *gorm.DB, modelKey string) (serviceID, hiProviderID, loProviderID uint64) {
	t.Helper()

	require.NoError(t, db.Exec(
		`INSERT INTO ai_service (model_key, display_name, service_type, is_active) VALUES (?, ?, 'llm', 1)`,
		modelKey, modelKey+"-display").Error)
	require.NoError(t, db.Raw(`SELECT id FROM ai_service WHERE model_key=?`, modelKey).Scan(&serviceID).Error)
	require.NotZero(t, serviceID)

	mkProvider := func(name, baseURL, key string) uint64 {
		require.NoError(t, db.Exec(
			`INSERT INTO llm_provider (name, base_url, api_key, is_active) VALUES (?, ?, ?, 1)`,
			name, baseURL, key).Error)
		var id uint64
		require.NoError(t, db.Raw(`SELECT id FROM llm_provider WHERE name=?`, name).Scan(&id).Error)
		require.NotZero(t, id)
		return id
	}
	hiProviderID = mkProvider("prov-hi-"+modelKey, "https://hi.invalid", "hi-key")
	loProviderID = mkProvider("prov-lo-"+modelKey, "https://lo.invalid", "lo-key")

	mkRoute := func(providerID uint64, pmi string, priority int) {
		require.NoError(t, db.Exec(
			`INSERT INTO ai_service_route (model_id, provider_id, provider_model_id, priority, is_active) VALUES (?, ?, ?, ?, 1)`,
			serviceID, providerID, pmi, priority).Error)
	}
	mkRoute(hiProviderID, modelKey, 10)
	mkRoute(loProviderID, modelKey+"-guan", 5)
	return serviceID, hiProviderID, loProviderID
}

// TestStore_ListResolvedRoutesByModel_ReturnsAllRoutesByPriority verifies the
// store returns BOTH provider routes for a model (no LIMIT 1), priority DESC.
func TestStore_ListResolvedRoutesByModel_ReturnsAllRoutesByPriority(t *testing.T) {
	db := newStoreTestDB(t)
	serviceID, hiP, loP := seedModelTwoProviders(t, db, "deepseek-v4-pro-test")
	store := NewStore(db)

	rows, err := store.ListResolvedRoutesByModel(context.Background(), serviceID)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	assert.Equal(t, hiP, rows[0].ProviderID, "highest priority route first")
	assert.Equal(t, 10, rows[0].RoutePriority)
	assert.Equal(t, loP, rows[1].ProviderID, "alternate provider second")
	assert.Equal(t, 5, rows[1].RoutePriority)
	assert.Equal(t, "deepseek-v4-pro-test-guan", rows[1].ProviderModelID, "alternate carries its own provider_model_id")
}

// TestStore_ListResolvedRoutesByModel_EmptyForUnknown: unknown service → empty,
// not an error.
func TestStore_ListResolvedRoutesByModel_EmptyForUnknown(t *testing.T) {
	db := newStoreTestDB(t)
	rows, err := NewStore(db).ListResolvedRoutesByModel(context.Background(), 99999)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestStore_ListResolvedRoutesByModel_SkipsInactiveRoute: inactive routes are
// excluded by the JOIN filter (r.is_active = true).
func TestStore_ListResolvedRoutesByModel_SkipsInactiveRoute(t *testing.T) {
	db := newStoreTestDB(t)
	serviceID, hiP, _ := seedModelTwoProviders(t, db, "model-inactive-alt")
	require.NoError(t, db.Exec(`UPDATE ai_service_route SET is_active=0 WHERE model_id=? AND priority=5`, serviceID).Error)

	rows, err := NewStore(db).ListResolvedRoutesByModel(context.Background(), serviceID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, hiP, rows[0].ProviderID)
}

// TestResolveModelAlternates_ExcludesPrimaryProvider: the registry method returns
// the same-model routes minus the provider already attempted as primary, carrying
// the taskID into each ResolvedRoute.
func TestResolveModelAlternates_ExcludesPrimaryProvider(t *testing.T) {
	db := newStoreTestDB(t)
	serviceID, hiP, loP := seedModelTwoProviders(t, db, "model-alt-exclude")
	reg := NewWithStore(NewStore(db), time.Minute)

	// Exclude the hi provider (primary) → only the lo (dmxapi-like) route remains.
	alts, err := reg.ResolveModelAlternates(context.Background(), "agent.run", serviceID, hiP)
	require.NoError(t, err)
	require.Len(t, alts, 1)
	assert.Equal(t, loP, alts[0].Provider.ID)
	assert.Equal(t, "agent.run", alts[0].TaskID)
	assert.Equal(t, "model-alt-exclude-guan", alts[0].ProviderModelID)
	assert.Equal(t, "https://lo.invalid", alts[0].Provider.BaseURL, "alternate carries its own endpoint")

	// Excluding the lo provider returns the hi route instead.
	alts2, err := reg.ResolveModelAlternates(context.Background(), "agent.run", serviceID, loP)
	require.NoError(t, err)
	require.Len(t, alts2, 1)
	assert.Equal(t, hiP, alts2[0].Provider.ID)
}

// TestResolveModelAlternates_EmptyWhenSingleProvider: a model with one provider
// has no alternates once that provider is excluded.
func TestResolveModelAlternates_EmptyWhenSingleProvider(t *testing.T) {
	db := newStoreTestDB(t)
	serviceID := seedRouteWithThinkingFlags(t, db, "single-prov-model", false, false, "")
	reg := NewWithStore(NewStore(db), time.Minute)

	rows, err := NewStore(db).ListResolvedRoutesByModel(context.Background(), serviceID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	alts, err := reg.ResolveModelAlternates(context.Background(), "agent.run", serviceID, rows[0].ProviderID)
	require.NoError(t, err)
	assert.Empty(t, alts)
}
