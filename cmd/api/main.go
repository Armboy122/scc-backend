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

	"github.com/smartcover/backend/internal/application/auth"
	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	dashApp "github.com/smartcover/backend/internal/application/dashboard"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	"github.com/smartcover/backend/internal/config"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/smartcover/backend/internal/infrastructure/storage"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
	"github.com/smartcover/backend/internal/interfaces/http/server"
)

func main() {
	cfg := config.Load()

	// Database
	db, err := persistence.InitDB(cfg.DatabaseURL, cfg.SeedData)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}

	// Repositories
	userRepo := persistence.NewGormUserRepo(db)
	tokenRepo := persistence.NewGormRefreshTokenRepo(db)
	coverRepo := persistence.NewGormCoverRepo(db)
	woRepo := persistence.NewGormWorkOrderRepo(db)
	borrowRepo := persistence.NewGormBorrowRepo(db)
	officeRepo := persistence.NewGormOfficeRepo(db)
	hubRepo := persistence.NewGormWorkHubRepo(db)
	notifRepo := persistence.NewGormNotificationRepo(db)

	// MinIO
	minioClient, err := storage.NewMinioClient(
		cfg.MinioEndpoint,
		cfg.MinioAccessKey,
		cfg.MinioSecretKey,
		cfg.MinioBucket,
		cfg.MinioPublicURL,
		cfg.MinioUseSSL,
	)
	if err != nil {
		log.Printf("warn: minio init failed: %v (uploads will not work)", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := minioClient.CreateBucketIfNotExists(ctx); err != nil {
			log.Printf("warn: minio bucket init: %v", err)
		}
	}

	// Services
	authSvc := auth.NewService(userRepo, tokenRepo, cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	coverSvc := coverApp.NewService(coverRepo)
	woSvc := woApp.NewService(woRepo, coverRepo, db, notifRepo)
	borrowSvc := borrowApp.NewService(borrowRepo, db, notifRepo)
	dashSvc := dashApp.NewService(coverSvc, officeRepo, woRepo)
	cronSvc := woApp.NewCronService(woRepo, notifRepo)
	borrowCronSvc := borrowApp.NewCronService(borrowSvc)
	cronCtx, stopCron := context.WithCancel(context.Background())
	defer stopCron()
	go cronSvc.Start(cronCtx, time.Hour)
	go borrowCronSvc.Start(cronCtx, time.Hour)

	// Handlers
	authHandler := handler.NewAuthHandler(authSvc)
	coverHandler := handler.NewCoverHandler(coverSvc)
	stockHandler := handler.NewStockHandler(coverSvc, officeRepo)
	woHandler := handler.NewWorkOrderHandler(woSvc)
	borrowHandler := handler.NewBorrowHandler(borrowSvc)
	notifHandler := handler.NewNotificationHandler(notifRepo)
	dashHandler := handler.NewDashboardHandler(dashSvc)
	expansionHandler := handler.NewExpansionHandler(coverSvc, officeRepo, woRepo)
	adminHandler := handler.NewAdminHandler(userRepo, officeRepo, hubRepo)
	healthHandler := handler.NewHealthHandler()

	var uploadHandler *handler.UploadHandler
	if minioClient != nil {
		uploadHandler = handler.NewUploadHandler(minioClient)
	} else {
		uploadHandler = handler.NewUploadHandler(nil)
	}

	// Router
	r := server.NewRouter(server.Dependencies{
		AuthSvc:          authSvc,
		AuthHandler:      authHandler,
		CoverHandler:     coverHandler,
		StockHandler:     stockHandler,
		WOHandler:        woHandler,
		BorrowHandler:    borrowHandler,
		ExpansionHandler: expansionHandler,
		UploadHandler:    uploadHandler,
		NotifHandler:     notifHandler,
		DashHandler:      dashHandler,
		AdminHandler:     adminHandler,
		HealthHandler:    healthHandler,
		CORSOrigins:      cfg.CORSOrigins,
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("server listening on %s (env=%s)", addr, cfg.Env)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	stopCron()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server shutdown: %v", err)
	}
	log.Println("server stopped")
}
