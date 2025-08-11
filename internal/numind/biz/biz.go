package biz

//go:generate mockgen -destination mock_biz.go -package biz github.com/marmotedu/miniblog/internal/miniblog/biz IBiz

import (
	accountrecordbiz "numind-server/internal/numind/biz/account_record"
	"numind-server/internal/numind/biz/admin"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/article"
	"numind-server/internal/numind/biz/baidu"
	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/category"
	"numind-server/internal/numind/biz/chat"
	"numind-server/internal/numind/biz/feedback"
	"numind-server/internal/numind/biz/image"
	"numind-server/internal/numind/biz/mqtt"
	"numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/numind/biz/post"
	"numind-server/internal/numind/biz/template"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/biz/wechat"
	"numind-server/internal/numind/store"

	"github.com/spf13/viper"
)

// IBiz 定义了 Biz 层需要实现的方法.
type IBiz interface {
	Users() user.UserBiz
	Posts() post.PostBiz
	Images() image.ImageBiz
	Cards() card.CardBiz
	Books() book.BookBiz
	Categories() category.CategoryBiz
	Templates() template.TemplateBiz
	Feedbacks() feedback.FeedbackBiz
	Baidu() baidu.BaiduBiz
	Wechat() wechat.WechatBiz
	Ali() ali.AliBiz
	Volc() volc.VolcBiz
	Mqtt() mqtt.MqttBiz
	Pagination() pagination.PaginationBiz
	Chats() chat.ChatBiz
	Article() article.IArticleBiz
	Admin() admin.IAdminBiz
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// biz 是 IBiz 的一个具体实现.
type biz struct {
	ds      store.IStore
	mqttBiz mqtt.MqttBiz
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// NewBiz 创建一个 IBiz 类型的实例.
func NewBiz(ds store.IStore) *biz {
	b := &biz{ds: ds}

	// 初始化MQTT连接（可选，不强制连接）
	mqttBiz := mqtt.NewMqttBiz()
	// 注释掉强制连接，改为按需连接
	// if err := mqttBiz.Connect(); err != nil {
	// 	log.Errorw("Failed to connect to MQTT broker", "error", err.Error())
	// }
	b.mqttBiz = mqttBiz

	return b
}

// Users 返回一个实现了 UserBiz 接口的实例.
func (b *biz) Users() user.UserBiz {
	return user.New(b.ds)
}

// Posts 返回一个实现了 PostBiz 接口的实例.
func (b *biz) Posts() post.PostBiz {
	return post.New(b.ds)
}

func (b *biz) Images() image.ImageBiz {
	return image.New(b.ds)
}

func (b *biz) Cards() card.CardBiz {
	return card.New(b.ds)
}

func (b *biz) Books() book.BookBiz {
	return book.New(b.ds)
}

func (b *biz) Categories() category.CategoryBiz {
	return category.New(b.ds)
}

func (b *biz) Templates() template.TemplateBiz {
	return template.New(b.ds)
}

func (b *biz) Feedbacks() feedback.FeedbackBiz {
	return feedback.New(b.ds)
}

func (b *biz) Baidu() baidu.BaiduBiz {
	return baidu.New(
		viper.GetString("baidu.api_key"),
		viper.GetString("baidu.secret_key"),
	)
}

func (b *biz) Wechat() wechat.WechatBiz {
	return wechat.New(b.ds)
}

func (b *biz) Ali() ali.AliBiz {
	return ali.NewAliBiz(b.ds)
}

func (b *biz) Volc() volc.VolcBiz {
	return volc.NewVolcBiz(b.ds)
}

func (b *biz) Order() order.OrderBiz {
	return order.NewOrderBiz(b.ds, b.Users(), b.AccountRecords())
}

func (b *biz) AccountRecords() accountrecordbiz.AccountRecordBiz {
	return accountrecordbiz.NewAccountRecordBiz(b.ds)
}

func (b *biz) Mqtt() mqtt.MqttBiz {
	return b.mqttBiz
}

func (b *biz) Pagination() pagination.PaginationBiz {
	return pagination.NewPaginationBiz()
}

func (b *biz) Chats() chat.ChatBiz {
	return chat.New(b.ds)
}

func (b *biz) Article() article.IArticleBiz {
	return article.NewArticleBiz(b.ds.Article())
}

func (b *biz) Admin() admin.IAdminBiz {
	return admin.NewAdminBiz(b.ds.Admin())
}
