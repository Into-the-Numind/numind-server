package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"numind-server/configs/config"
	"numind-server/internal/database"
	"numind-server/internal/handlers"
	"numind-server/internal/middleware"
	"numind-server/internal/services"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/robfig/cron/v3"
)

func main() {
	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	// 初始化配置
	cfg := config.Load()

	// 初始化数据库
	db, err := database.InitDB(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 自动迁移数据库表
	if err := database.AutoMigrate(db); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	// 初始化服务
	authService := services.NewAuthService(db, cfg)
	articleService := services.NewArticleService(db, cfg)
	adminService := services.NewAdminService(db, cfg)
	configService := services.NewConfigService(db)

	// 初始化处理器
	authHandler := handlers.NewAuthHandler(authService)
	articleHandler := handlers.NewArticleHandler(articleService)
	adminHandler := handlers.NewAdminHandler(adminService)
	configHandler := handlers.NewConfigHandler(configService)

	// 设置Gin模式
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建Gin引擎
	r := gin.Default()

	// 配置CORS
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173", "https://llm-soup.xyz", "http://47.108.85.126"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// 全局中间件
	r.Use(middleware.Logger())
	r.Use(middleware.ErrorHandler())

	// 根路由
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "欢迎使用Numind API",
		})
	})

	// 注册路由组
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/wechat-login", authHandler.WechatLogin)
		authGroup.PUT("/update-profile", middleware.AuthMiddleware(authService), authHandler.UpdateProfile)
		authGroup.GET("/profile", middleware.OptionalAuthMiddleware(authService), authHandler.GetProfile)
		authGroup.GET("/profile/stats", middleware.OptionalAuthMiddleware(authService), authHandler.GetUserStats)
		authGroup.POST("/avatar/upload", middleware.AuthMiddleware(authService), authHandler.UploadAvatar)
		authGroup.POST("/get-phone", middleware.AuthMiddleware(authService), authHandler.GetPhoneNumber)
		authGroup.PUT("/update-phone", middleware.AuthMiddleware(authService), authHandler.UpdatePhone)
		authGroup.POST("/admin-login", authHandler.AdminLogin)
		authGroup.PUT("/change-password", middleware.AuthMiddleware(authService), authHandler.ChangePassword)
	}

	articleGroup := r.Group("/articles")
	{
		articleGroup.POST("/fetch", middleware.AuthMiddleware(authService), articleHandler.FetchArticle)
		articleGroup.GET("", middleware.OptionalAuthMiddleware(authService), articleHandler.GetArticles)
		articleGroup.GET("/:id", middleware.OptionalAuthMiddleware(authService), articleHandler.GetArticle)
		articleGroup.PUT("/:id/category", middleware.AuthMiddleware(authService), articleHandler.UpdateArticleCategory)
		articleGroup.DELETE("/:id", middleware.AuthMiddleware(authService), articleHandler.DeleteArticle)
		articleGroup.POST("/:id/favorite", middleware.AuthMiddleware(authService), articleHandler.AddFavorite)
		articleGroup.DELETE("/:id/favorite", middleware.AuthMiddleware(authService), articleHandler.RemoveFavorite)
		articleGroup.GET("/favorites", middleware.AuthMiddleware(authService), articleHandler.GetFavorites)
		articleGroup.POST("/paraphrase", middleware.AuthMiddleware(authService), articleHandler.ParaphraseText)
	}

	adminGroup := r.Group("/admin")
	{
		adminGroup.Use(middleware.AdminAuthMiddleware(authService))
		adminGroup.GET("/articles", adminHandler.GetArticles)
		adminGroup.GET("/articles/:id", adminHandler.GetArticle)
		adminGroup.POST("/articles", adminHandler.CreateArticle)
		adminGroup.PUT("/articles/:id", adminHandler.UpdateArticle)
		adminGroup.DELETE("/articles/:id", adminHandler.DeleteArticle)
		adminGroup.POST("/articles/bulk-delete", adminHandler.BulkDeleteArticles)
		adminGroup.GET("/users", adminHandler.GetUsers)
		adminGroup.PUT("/users/:id", adminHandler.UpdateUser)
		adminGroup.DELETE("/users/:id", adminHandler.DeleteUser)
		adminGroup.GET("/categories", adminHandler.GetCategories)
		adminGroup.POST("/categories", adminHandler.CreateCategory)
		adminGroup.PUT("/categories/:id", adminHandler.UpdateCategory)
		adminGroup.DELETE("/categories/:id", adminHandler.DeleteCategory)
		//adminGroup.GET("/proxies", adminHandler.GetProxies)
		//adminGroup.POST("/proxies", adminHandler.CreateProxy)
		//adminGroup.PUT("/proxies/:id", adminHandler.UpdateProxy)
		//adminGroup.DELETE("/proxies/:id", adminHandler.DeleteProxy)
		//adminGroup.POST("/proxies/bulk-delete", adminHandler.BulkDeleteProxies)
		//adminGroup.GET("/feedbacks", adminHandler.GetFeedbacks)
		//adminGroup.PUT("/feedbacks/:id", adminHandler.UpdateFeedback)
		//adminGroup.DELETE("/feedbacks/:id", adminHandler.DeleteFeedback)
		//adminGroup.POST("/feedbacks/bulk-delete", adminHandler.BulkDeleteFeedbacks)
		//adminGroup.GET("/about-us", adminHandler.GetAboutUs)
		//adminGroup.PUT("/about-us", adminHandler.UpdateAboutUs)
		//adminGroup.GET("/agreements/:type", adminHandler.GetAgreement)
		//adminGroup.PUT("/agreements/:type", adminHandler.UpdateAgreement)
		adminGroup.GET("/stats", adminHandler.GetStats)
	}

	configGroup := r.Group("/config")
	{
		configGroup.GET("", configHandler.GetConfigs)
		configGroup.PUT("/:key", middleware.AdminAuthMiddleware(authService), configHandler.UpdateConfig)
	}

	// 启动后台任务
	startBackgroundTasks(adminService)

	// 启动服务器
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", cfg.Server.Port),
		Handler: r,
	}

	// 优雅关闭
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on port %s", cfg.Server.Port)

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// 设置关闭超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exited")
}

func startBackgroundTasks(adminService *services.AdminService) {
	c := cron.New()

	// 每小时更新代理池
	if _, err := c.AddFunc("0 * * * *", func() {
		if err := adminService.FetchNewProxies(); err != nil {
			log.Printf("Failed to fetch new proxies: %v", err)
		}
	}); err != nil {
		log.Printf("Failed to add proxy fetch cron job: %v", err)
	}

	// 每天凌晨2点清理过期数据
	if _, err := c.AddFunc("0 2 * * *", func() {
		if err := adminService.CleanupExpiredData(); err != nil {
			log.Printf("Failed to cleanup expired data: %v", err)
		}
	}); err != nil {
		log.Printf("Failed to add cleanup cron job: %v", err)
	}

	c.Start()
}
