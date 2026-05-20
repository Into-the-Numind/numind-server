package budget

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultAdminTestGrantConstants(t *testing.T) {
	assert.Equal(t, uint32(5000), DefaultAdminTestGrant)
	assert.Equal(t, int64(5000), DefaultAdminTestGrantInt64)
	// sanity: two consts must be the same numeric value
	assert.Equal(t, int64(DefaultAdminTestGrant), DefaultAdminTestGrantInt64)
}
