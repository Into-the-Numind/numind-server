package aiservice

import (
	"context"

	"github.com/spf13/viper"
	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
)

// SyncProviderCredentials syncs provider API keys from application config into the
// llm_provider table (DB). This function is intended to be called once at service
// startup, after the DB connection is established but before routes are registered.
//
// Design rationale (spec §2.6):
//   - Migration SQL inserts provider rows with empty api_key strings.
//   - On each startup, this function UPSERTs api_key values from config into the DB.
//   - This makes key rotation easy: update config → restart service → keys are live.
//   - The DB is the single source of truth at runtime; config is only read at startup.
//
// Failure behaviour: errors are logged but do NOT prevent service startup.
// The /healthz/ai endpoint (Task 8) exposes seed status for monitoring.
//
// TODO(Task 8): Implement the actual UPSERT logic using viper config + gorm DB.
func SyncProviderCredentials(ctx context.Context, db *gorm.DB, cfg *viper.Viper) error {
	// Placeholder implementation — Task 8 will fill this in.
	log.Infow("[aiservice] SyncProviderCredentials: stub — no-op until Task 8")
	return nil
}
