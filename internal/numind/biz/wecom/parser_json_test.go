package wecom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const sampleMergedMsgJSON = `{
	"msgtype": "chatrecord",
	"chatrecord": {
		"item": [
			{
				"type": "ChatRecordText",
				"msgtime": 1603875610,
				"content": "{\"content\":\"test text from seller\"}",
				"sourcename": "SellerA"
			},
			{
				"type": "ChatRecordText",
				"msgtime": 1603875620,
				"content": "{\"content\":\"test text from customer\"}",
				"sourcename": "CustomerB"
			}
		]
	}
}`

func TestParseMergedHistoryJSON(t *testing.T) {
	msgs, err := ParseMergedHistoryJSON(sampleMergedMsgJSON)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(msgs))

	assert.Equal(t, "SellerA", msgs[0].Speaker)
	assert.Equal(t, "test text from seller", msgs[0].Content)

	assert.Equal(t, "CustomerB", msgs[1].Speaker)
	assert.Equal(t, "test text from customer", msgs[1].Content)
}
