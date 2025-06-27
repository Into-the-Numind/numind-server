package v1

type ConfigUpdateRequest struct {
	Value       string `json:"value" binding:"required"`
	Description string `json:"description"`
}
