package biz

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/store"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newFeishuCompositionDeps(t *testing.T) feishuCompositionDeps {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "composition.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ds := store.NewTestStore(db)
	resumeStore, ok := ds.AgentRuns().(store.IExternalToolResumeLease)
	require.True(t, ok)
	return feishuCompositionDeps{
		enabled:       true,
		dataStore:     ds,
		tokenKey:      base64.StdEncoding.EncodeToString(make([]byte, 32)),
		keyVersion:    "test-v1",
		runtimeBase:   filepath.Join(t.TempDir(), "runtime"),
		authOwner:     "test-feishu-auth-worker",
		studentRuns:   agent.NewStudentRunService(nil, ds.AgentRuns(), ds.AgentDefinitions(), nil, nil, nil),
		resumeStore:   resumeStore,
		supervisor:    agent.NewExternalContinuationSupervisor(agent.ExternalContinuationLimit),
		verifyVersion: func(context.Context) error { return nil },
	}
}

func TestBuildFeishuService_FeatureFlagOffReturnsNil(t *testing.T) {
	composition, err := buildFeishuService(feishuCompositionDeps{enabled: false})
	require.NoError(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_VersionMismatchFailsClosed(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.verifyVersion = func(context.Context) error { return errors.New("wrong CLI version") }

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_ComposesCompleteWorkspaceBeforePublishing(t *testing.T) {
	composition, err := buildFeishuService(newFeishuCompositionDeps(t))
	require.NoError(t, err)
	require.NotNil(t, composition)
	require.NotNil(t, composition.skillReader)
	require.NotNil(t, composition.operationService)
	require.NotNil(t, composition.authSessionService)
	require.NotNil(t, composition.resumer)
	require.NotNil(t, composition.dispatcher)
	require.Same(t, composition.dispatcher, composition.authWorkerDispatcher)
	require.NotNil(t, composition.supervisor)
}

func TestFeishuWorkspacePublication_UsesOneComposedGraph(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	composition, err := buildFeishuService(deps)
	require.NoError(t, err)

	b := &biz{}
	b.publishFeishuPersonalWorkspace(composition, deps.resumeStore)

	// Task 13's future HTTP service has one private source, while the auth
	// worker and process reclaimer keep the exact dispatcher/resumer graph that
	// composition created. No second service construction is necessary.
	require.Same(t, composition, b.feishuWorkspace)
	require.Same(t, composition.supervisor, b.externalResumeSupervisor)
	require.NotNil(t, b.externalResumeReclaimer)
	require.Same(t, composition.dispatcher, composition.authWorkerDispatcher)
}

func TestFeishuWorkspacePublication_FailedCompositionLeavesBizUnpublished(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.verifyVersion = func(context.Context) error { return errors.New("wrong CLI version") }
	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)

	b := &biz{}
	b.publishFeishuPersonalWorkspace(composition, deps.resumeStore)
	require.Nil(t, b.feishuWorkspace)
	require.Nil(t, b.externalResumeSupervisor)
	require.Nil(t, b.externalResumeReclaimer)
}

func TestBuildFeishuService_MissingExplicitRuntimeRootFailsClosed(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.runtimeBase = ""

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_CleanupFailurePreventsPublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "runtime-link")
	require.NoError(t, os.Symlink(outside, link))
	deps.runtimeBase = link

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}
