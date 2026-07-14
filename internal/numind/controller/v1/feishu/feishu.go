// Package feishu contains thin, authenticated HTTP adapters for a personal
// Feishu workspace. All lifecycle state, ownership, generation fencing, vault
// cleanup, and Agent continuation logic remains in biz/feishu.
package feishu

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	feishubiz "numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// Controller wires the Feishu lifecycle HTTP handlers to the already-composed
// biz service. It deliberately contains no state-machine or runner behavior.
type Controller struct {
	svc feishubiz.IFeishuService
}

// NewController constructs a Controller from the complete lifecycle service.
func NewController(svc feishubiz.IFeishuService) *Controller {
	return &Controller{svc: svc}
}

type connectRequest struct {
	Intent string `json:"intent"`
}

type resumeRequest struct {
	Action string `json:"action"`
}

// Connect handles POST /v1/feishu/connect. The only accepted body is the
// explicit manual intent; scopes, argv, app ids, operation ids, and user ids
// are rejected before the service is called.
func (h *Controller) Connect(c *gin.Context) {
	user, ok := lifecycleUser(c)
	if !ok {
		return
	}
	var request connectRequest
	if err := decodeStrictJSON(c, &request); err != nil || request.Intent != "manual" {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	result, err := h.svc.Connect(c.Request.Context(), user.ID)
	writeLifecycleResponse(c, err, result)
}

// Status handles GET /v1/feishu/status. It has no body and invokes the
// read-only service method; it cannot create an auth URL/worker.
func (h *Controller) Status(c *gin.Context) {
	user, ok := lifecycleUser(c)
	if !ok {
		return
	}
	result, err := h.svc.Status(c.Request.Context(), user.ID)
	writeLifecycleResponse(c, err, result)
}

// ResumeOperation handles POST /v1/feishu/operations/:id/resume. It only
// permits fixed external-action acknowledgements; no command data can cross
// this controller boundary.
func (h *Controller) ResumeOperation(c *gin.Context) {
	user, ok := lifecycleUser(c)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(c.Param("id"))
	var request resumeRequest
	if operationID == "" || decodeStrictJSON(c, &request) != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	result, err := h.svc.Resume(c.Request.Context(), user.ID, operationID, request.Action)
	writeLifecycleResponse(c, err, result)
}

// RefreshAction handles POST /v1/feishu/actions/:session_id/refresh. The
// session id comes exclusively from the route; its body must be empty so a
// client cannot submit device codes, URLs, scopes, or another tenant id.
func (h *Controller) RefreshAction(c *gin.Context) {
	user, ok := lifecycleUser(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" || requireEmptyBody(c) != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	result, err := h.svc.RefreshAction(c.Request.Context(), user.ID, sessionID)
	writeLifecycleResponse(c, err, result)
}

// Unbind handles DELETE /v1/feishu/connection. The result explicitly says the
// Numind-side vault/connection was removed while the remote personal app stays
// in Feishu for the user to manage.
func (h *Controller) Unbind(c *gin.Context) {
	user, ok := lifecycleUser(c)
	if !ok {
		return
	}
	if requireEmptyBody(c) != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	result, err := h.svc.Unbind(c.Request.Context(), user.ID)
	writeLifecycleResponse(c, err, result)
}

func lifecycleUser(c *gin.Context) (*model.User, bool) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return nil, false
	}
	return user, true
}

func decodeStrictJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errno.ErrInvalidParameter
	}
	return nil
}

func requireEmptyBody(c *gin.Context) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(c.Request.Body)
	var value any
	if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
		return nil
	}
	return errno.ErrInvalidParameter
}

func writeLifecycleResponse(c *gin.Context, err error, data any) {
	switch {
	case errors.Is(err, feishubiz.ErrWorkspaceLifecycleNotFound):
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	case errors.Is(err, feishubiz.ErrWorkspaceLifecycleInvalid):
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
	case errors.Is(err, feishubiz.ErrWorkspaceLifecycleUnavailable):
		core.WriteResponse(c, errno.ErrInternalServer, nil)
	default:
		core.WriteResponse(c, err, data)
	}
}
