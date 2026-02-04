package wecom

import (
	"numind-server/internal/numind/store"
)

// WecomBiz 定义企业微信业务层接口
type WecomBiz interface {
	GetBindingByNumindUser(numindUserID int64) (*WecomUser, error)
	GetContacts(numindUserID int64) ([]ContactConversation, error)
	GetMessagesByNumindUser(numindUserID int64, limit, offset int) ([]WecomMessage, int64, error)
	GetConversationMessages(externalUserID, partnerID string, limit, offset int) ([]WecomMessage, int64, error)
	GenerateBindCode(numindUserID int64) (string, error)
	VerifyAndBind(code string, externalUserID string) error

	// Inbox / Import Batch Methods
	GetArchiveSessions(userID int64) ([]ArchiveSession, error)
	GetSessionMessages(userID int64, sessionKey string) ([]ArchiveMessage, error)
}

type wecomBiz struct {
	*BindingService
	*ImportService
}

func New(ds store.IStore) WecomBiz {
	return &wecomBiz{
		BindingService: NewBindingService(ds.DB()),
		ImportService:  NewImportService(ds.DB()),
	}
}
