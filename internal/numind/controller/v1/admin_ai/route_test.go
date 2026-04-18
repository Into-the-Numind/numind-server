// Package admin_ai_test contains HTTP-handler level tests for route CRUD endpoints.
// These tests focus on response envelope shape rather than business logic (which is
// covered by biz_test.go in the biz layer).
package admin_ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admin_ai "numind-server/internal/numind/controller/v1/admin_ai"
	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/model"
)

// stubBiz is a minimal IAIServiceAdminBiz implementation that returns preset
// results for the methods exercised by RouteController handler tests.
// Methods not under test panic to surface accidental calls.
type stubBiz struct {
	toggleResult *aiservice_admin.RouteDTO
	toggleErr    error
}

func (s *stubBiz) ToggleRoute(_ context.Context, _ uint64, _ uint64, _ string) (*aiservice_admin.RouteDTO, error) {
	return s.toggleResult, s.toggleErr
}

// --- Unused methods required to satisfy the interface ---

func (s *stubBiz) ListServices(_ context.Context, _ registry.ServiceFilter, _, _ int) (*aiservice_admin.ListServicesResult, error) {
	panic("not implemented")
}
func (s *stubBiz) GetService(_ context.Context, _ uint64) (*aiservice_admin.ServiceDetail, error) {
	panic("not implemented")
}
func (s *stubBiz) CreateService(_ context.Context, _ *model.AIService, _ uint64, _ string) (*model.AIService, error) {
	panic("not implemented")
}
func (s *stubBiz) UpdateService(_ context.Context, _ *model.AIService, _ uint64, _ string) error {
	panic("not implemented")
}
func (s *stubBiz) DeprecateService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	panic("not implemented")
}
func (s *stubBiz) RestoreService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	panic("not implemented")
}
func (s *stubBiz) GetCapabilitySchemas(_ context.Context) (map[string]*profile.CapabilitySchema, error) {
	panic("not implemented")
}
func (s *stubBiz) ListTasks(_ context.Context) ([]*aiservice_admin.TaskProfileListItem, error) {
	panic("not implemented")
}
func (s *stubBiz) GetTask(_ context.Context, _ string) (*aiservice_admin.TaskDetail, error) {
	panic("not implemented")
}
func (s *stubBiz) UpdateTask(_ context.Context, _ string, _ aiservice_admin.UpdateTaskRequest, _ bool, _ uint64, _ string) (*aiservice_admin.UpdateTaskResult, error) {
	panic("not implemented")
}
func (s *stubBiz) ValidateServiceAgainstTask(_ context.Context, _ uint64, _ string) (*aiservice_admin.ValidateResult, error) {
	panic("not implemented")
}
func (s *stubBiz) CreateRoute(_ context.Context, _ uint64, _ aiservice_admin.CreateRouteRequest, _ uint64, _ string) (*aiservice_admin.RouteDTO, []string, error) {
	panic("not implemented")
}
func (s *stubBiz) UpdateRoute(_ context.Context, _ uint64, _ aiservice_admin.UpdateRouteRequest, _ uint64, _ string) (*aiservice_admin.RouteDTO, []string, error) {
	panic("not implemented")
}
func (s *stubBiz) DeleteRoute(_ context.Context, _ uint64, _ uint64, _ string) error {
	panic("not implemented")
}
func (s *stubBiz) ListAuditLogs(_ context.Context, _ aiservice_admin.AuditLogFilter, _, _ int) (*aiservice_admin.ListAuditLogsResult, error) {
	panic("not implemented")
}
func (s *stubBiz) ListProviders(_ context.Context) ([]aiservice_admin.ProviderDTO, error) {
	panic("not implemented")
}
func (s *stubBiz) GetProvider(_ context.Context, _ uint64) (*aiservice_admin.ProviderDTO, error) {
	panic("not implemented")
}
func (s *stubBiz) CreateProvider(_ context.Context, _ aiservice_admin.CreateProviderRequest, _ uint64, _ string) (*aiservice_admin.ProviderDTO, error) {
	panic("not implemented")
}
func (s *stubBiz) UpdateProvider(_ context.Context, _ uint64, _ aiservice_admin.UpdateProviderRequest, _ uint64, _ string) (*aiservice_admin.ProviderDTO, error) {
	panic("not implemented")
}
func (s *stubBiz) DeleteProvider(_ context.Context, _ uint64, _ uint64, _ string) error {
	panic("not implemented")
}
func (s *stubBiz) TestProviderConnection(_ context.Context, _ uint64) (aiservice_admin.TestConnectionResult, error) {
	panic("not implemented")
}

// ----------------------------------------------------------------------------
// TestToggleRouteHandler_ResponseEnvelope verifies that the Toggle handler wraps
// the route DTO in gin.H{"route": dto} to match the Create/Update envelope shape.
// Admin-web consumers must be able to use a single parse shape for all three endpoints.
// ----------------------------------------------------------------------------

func TestToggleRouteHandler_ResponseEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dto := &aiservice_admin.RouteDTO{
		ID:              42,
		ServiceID:       7,
		ProviderID:      3,
		ProviderName:    "Aliyun",
		ProviderModelID: "qwen-turbo",
		Priority:        0,
		IsActive:        false,
	}

	stub := &stubBiz{toggleResult: dto}
	ctrl := admin_ai.NewRouteController(stub)

	r := gin.New()
	r.POST("/routes/:route_id/toggle", ctrl.Toggle)

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/routes/42/toggle", nil)
	require.NoError(t, err)

	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "expected 200; body: %s", w.Body.String())

	// Parse the unified response envelope.
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Route *aiservice_admin.RouteDTO `json:"route"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&envelope))

	assert.Equal(t, 0, envelope.Code, "code must be 0 (success)")
	require.NotNil(t, envelope.Data.Route, `response data must have a "route" key`)
	assert.Equal(t, uint64(42), envelope.Data.Route.ID)
	assert.False(t, envelope.Data.Route.IsActive, "toggled route should be inactive")
}
