package models

import (
	"time"
)

// User 用户表
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	OpenID    string    `gorm:"uniqueIndex;size:50" json:"openid"`
	Phone     string    `gorm:"size:20;index" json:"phone"`
	Nickname  string    `gorm:"size:100" json:"nickname"`
	AvatarURL string    `gorm:"size:255" json:"avatar_url"`
	CreatedAt time.Time `json:"created_at"`
	IsActive  bool      `gorm:"default:true" json:"is_active"`

	// 管理员相关字段
	Username  string     `gorm:"size:50;uniqueIndex" json:"username"`
	Password  string     `gorm:"size:255" json:"-"`
	IsAdmin   bool       `gorm:"default:false" json:"is_admin"`
	Status    int        `gorm:"default:0" json:"status"`
	LastLogin *time.Time `json:"last_login"`

	// 关联关系
	Articles  []Article  `gorm:"foreignKey:UserID" json:"articles,omitempty"`
	Favorites []Favorite `gorm:"foreignKey:UserID" json:"favorites,omitempty"`
	Feedbacks []Feedback `gorm:"foreignKey:UserID" json:"feedbacks,omitempty"`
}

// Category 文章分类表
type Category struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:50;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`

	// 关联关系
	Articles []Article `gorm:"foreignKey:CategoryID" json:"articles,omitempty"`
}

// Article 文章表
type Article struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"index;not null" json:"user_id"`
	URL              string    `gorm:"size:255;index" json:"url"`
	Title            string    `gorm:"size:255" json:"title"`
	AccountName      string    `gorm:"size:100" json:"account_name"`
	PublishTime      string    `gorm:"size:50" json:"publish_time"`
	CategoryID       *uint     `gorm:"index" json:"category_id"`
	Content          JSON      `gorm:"type:json" json:"content"`
	ContentTxt       string    `gorm:"type:longtext" json:"content_txt"`
	Summary          string    `gorm:"type:longtext" json:"summary"`
	AnnotatedContent string    `gorm:"type:longtext" json:"annotated_content"`
	CreatedAt        time.Time `json:"created_at"`
	CategoryAt       time.Time `json:"category_at"`

	// 关联关系
	User      User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Category  *Category  `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	Favorites []Favorite `gorm:"foreignKey:ArticleID" json:"favorites,omitempty"`
}

// Favorite 收藏表
type Favorite struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	ArticleID uint      `gorm:"index" json:"article_id"`
	CreatedAt time.Time `json:"created_at"`

	// 关联关系
	User    User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Article Article `gorm:"foreignKey:ArticleID" json:"article,omitempty"`
}

// SystemConfig 系统配置表
type SystemConfig struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Key         string    `gorm:"size:100;uniqueIndex" json:"key"`
	Value       string    `gorm:"type:text" json:"value"`
	Description string    `gorm:"size:255" json:"description"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProxyServer 代理服务器表
type ProxyServer struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	IPAddress     string     `gorm:"size:50;index;not null" json:"ip_address"`
	Port          int        `gorm:"not null" json:"port"`
	Protocol      string     `gorm:"size:10;default:http" json:"protocol"`
	Username      string     `gorm:"size:100" json:"username"`
	Password      string     `gorm:"size:100" json:"password"`
	Location      string     `gorm:"size:100" json:"location"`
	Status        int        `gorm:"default:1;index" json:"status"`
	LastCheckTime *time.Time `json:"last_check_time"`
	SuccessRate   *int       `json:"success_rate"`
	CheckCount    int        `gorm:"default:0" json:"check_count"`
	SuccessCount  int        `gorm:"default:0" json:"success_count"`
	IsAutoAdded   int        `gorm:"default:0" json:"is_auto_added"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
	Remarks       string     `gorm:"size:255" json:"remarks"`
}

// Feedback 用户反馈表
type Feedback struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	Type      string    `gorm:"size:50;not null" json:"type"`
	Status    int       `gorm:"default:0" json:"status"`
	Reply     string    `gorm:"type:text" json:"reply"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`

	// 关联关系
	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// AboutUs 关于我们表
type AboutUs struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Content   string    `gorm:"type:longtext;not null" json:"content"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;not null" json:"updated_at"`
}

// Agreement 协议表
type Agreement struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:32;uniqueIndex;not null" json:"type"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// JSON 自定义JSON类型
type JSON []byte

func (j JSON) MarshalJSON() ([]byte, error) {
	if j == nil {
		return []byte("null"), nil
	}
	return j, nil
}

func (j *JSON) UnmarshalJSON(data []byte) error {
	if j == nil {
		return nil
	}
	*j = append((*j)[0:0], data...)
	return nil
}

// 表名方法
func (User) TableName() string {
	return "users"
}

func (Category) TableName() string {
	return "categories"
}

func (Article) TableName() string {
	return "articles"
}

func (Favorite) TableName() string {
	return "favorites"
}

func (SystemConfig) TableName() string {
	return "system_configs"
}

func (ProxyServer) TableName() string {
	return "proxy_servers"
}

func (Feedback) TableName() string {
	return "feedbacks"
}

func (AboutUs) TableName() string {
	return "about_us"
}

func (Agreement) TableName() string {
	return "agreements"
}
