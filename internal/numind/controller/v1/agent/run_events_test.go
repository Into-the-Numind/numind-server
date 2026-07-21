package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type controllerRunEventBroker struct {
	mu        sync.Mutex
	after     string
	subscribe int
}

func (*controllerRunEventBroker) Publish(context.Context, uint64, stream.Event) (string, error) {
	return "", nil
}

func (b *controllerRunEventBroker) Subscribe(_ context.Context, runID uint64, after string) (<-chan stream.PublishedEvent, error) {
	b.mu.Lock()
	b.after = after
	b.subscribe++
	b.mu.Unlock()
	event, err := stream.Encode(stream.EventTerminal, stream.TerminalPayload{
		Reason: string(agentbiz.TerminalCompleted),
	}, 1, runID, 0)
	if err != nil {
		return nil, err
	}
	ch := make(chan stream.PublishedEvent, 1)
	ch <- stream.PublishedEvent{Cursor: "2000-0", Event: event}
	close(ch)
	return ch, nil
}

func setupRunEventsRouter(t *testing.T, owner *model.User, broker stream.RunEventBroker) (*gin.Engine, uint64) {
	t.Helper()
	db := newCtrlTestDB(t)
	runID := seedCtrlRun(t, db, owner.ID, "events-session")
	ds := store.NewTestStore(db)
	service := agentbiz.NewStudentRunService(nil, ds.AgentRuns(), nil, nil, nil, nil).
		WithRunEventBroker(broker)
	controller := &StudentRunController{runSvc: service}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("current_user", owner)
		c.Next()
	})
	router.GET("/v1/agent-runs/:id/events", controller.SubscribeEvents)
	return router, runID
}

func TestSubscribeEvents_WritesCursorAndEventForOwner(t *testing.T) {
	broker := &controllerRunEventBroker{}
	owner := &model.User{}
	owner.ID = 7
	router, runID := setupRunEventsRouter(t, owner, broker)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/agent-runs/%d/events?after=1000-0", runID), nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.True(t, strings.Contains(recorder.Body.String(), "id: 2000-0\n"))
	assert.Contains(t, recorder.Body.String(), `"type":"terminal"`)
	broker.mu.Lock()
	assert.Equal(t, "1000-0", broker.after)
	broker.mu.Unlock()
}

func TestSubscribeEvents_RejectsCrossUserBeforeBroker(t *testing.T) {
	broker := &controllerRunEventBroker{}
	db := newCtrlTestDB(t)
	runID := seedCtrlRun(t, db, 7, "foreign-events-session")
	ds := store.NewTestStore(db)
	service := agentbiz.NewStudentRunService(nil, ds.AgentRuns(), nil, nil, nil, nil).
		WithRunEventBroker(broker)
	controller := &StudentRunController{runSvc: service}
	router := gin.New()
	otherUser := &model.User{}
	otherUser.ID = 8
	router.Use(func(c *gin.Context) {
		c.Set("current_user", otherUser)
		c.Next()
	})
	router.GET("/v1/agent-runs/:id/events", controller.SubscribeEvents)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/agent-runs/%d/events", runID), nil)

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	broker.mu.Lock()
	assert.Zero(t, broker.subscribe)
	broker.mu.Unlock()
}
