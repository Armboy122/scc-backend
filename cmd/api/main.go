package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	migrationFiles "github.com/smartcover/backend/db/migrations"
	"github.com/smartcover/backend/internal/application/auth"
	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	dashApp "github.com/smartcover/backend/internal/application/dashboard"
	discrepancyApp "github.com/smartcover/backend/internal/application/discrepancy"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	"github.com/smartcover/backend/internal/config"
	"github.com/smartcover/backend/internal/infrastructure/migration"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/smartcover/backend/internal/infrastructure/storage"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
	"github.com/smartcover/backend/internal/interfaces/http/server"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if err := validateMigrationMode(cfg.Env, cfg.AutoMigrate); err != nil {
		log.Fatal(err)
	}

	// Database
	db, err := persistence.InitDB(cfg.DatabaseURL, cfg.SeedData, cfg.AutoMigrate)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("get database connection pool: %v", err)
	}
	if isProductionEnvironment(cfg.Env) {
		runner, err := migration.New(sqlDB, migrationFiles.Files)
		if err != nil {
			log.Fatalf("load production migration manifest: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := runner.RequireCurrent(ctx); err != nil {
			cancel()
			log.Fatalf("production schema is not current: %v", err)
		}
		cancel()
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
	minioClient, err := storage.NewMinioClientWithConfig(storage.MinioConfig{
		InternalEndpoint: cfg.MinioInternalEndpoint,
		PublicEndpoint:   cfg.MinioPublicEndpoint,
		AccessKey:        cfg.MinioAccessKey,
		SecretKey:        cfg.MinioSecretKey,
		Bucket:           cfg.MinioBucket,
		InternalSecure:   cfg.MinioInternalUseSSL,
		PublicSecure:     cfg.MinioPublicUseSSL,
	})
	if err != nil {
		if isProductionEnvironment(cfg.Env) {
			log.Fatalf("init private evidence storage: %v", err)
		}
		log.Printf("warn: minio init failed: %v (evidence will not work)", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := initializeEvidenceStorage(ctx, cfg.Env, minioClient); err != nil {
			if isProductionEnvironment(cfg.Env) {
				log.Fatalf("verify private evidence bucket through internal endpoint: %v", err)
			}
			log.Printf("warn: minio bucket init: %v", err)
		}
	}

	// Services
	authSvc := auth.NewService(userRepo, tokenRepo, cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	coverSvc := coverApp.NewService(coverRepo, woRepo)
	var woSvc *woApp.Service
	if minioClient != nil {
		woSvc = woApp.NewServiceWithEvidenceStore(woRepo, coverRepo, db, minioClient, notifRepo)
	} else {
		woSvc = woApp.NewService(woRepo, coverRepo, db, notifRepo)
	}
	borrowSvc := borrowApp.NewService(borrowRepo, db, notifRepo)
	discrepancySvc := discrepancyApp.NewService(db)
	dashSvc := dashApp.NewService(coverSvc, officeRepo, woRepo)
	cronSvc := woApp.NewCronService(woRepo, notifRepo, db)
	borrowCronSvc := borrowApp.NewCronService(borrowSvc)
	cronCtx, stopCron := context.WithCancel(context.Background())
	defer stopCron()
	startScheduledJobs(
		cronCtx,
		cfg.RunBackgroundJobs,
		cfg.EnablePhase2Borrowing,
		cronSvc,
		borrowCronSvc,
		time.Hour,
	)

	// Handlers
	authHandler := handler.NewAuthHandler(authSvc)
	coverHandler := handler.NewCoverHandler(coverSvc, officeRepo)
	stockHandler := handler.NewStockHandler(coverSvc, officeRepo)
	woHandler := handler.NewWorkOrderHandler(woSvc, officeRepo)
	borrowHandler := handler.NewBorrowHandler(borrowSvc)
	discrepancyHandler := handler.NewDiscrepancyHandler(discrepancySvc)
	notifHandler := handler.NewNotificationHandler(notifRepo)
	dashHandler := handler.NewDashboardHandler(dashSvc)
	expansionHandler := handler.NewExpansionHandler(coverSvc, officeRepo, woRepo)
	adminHandler := handler.NewAdminHandler(userRepo, officeRepo, hubRepo, tokenRepo)
	healthHandler := handler.NewHealthHandler(sqlDB, minioClient)

	uploadHandler := handler.NewUploadHandler(woSvc)

	// Router
	r := server.NewRouter(server.Dependencies{
		AuthSvc:                authSvc,
		AuthHandler:            authHandler,
		CoverHandler:           coverHandler,
		StockHandler:           stockHandler,
		WOHandler:              woHandler,
		BorrowHandler:          borrowHandler,
		DiscrepancyHandler:     discrepancyHandler,
		ExpansionHandler:       expansionHandler,
		UploadHandler:          uploadHandler,
		NotifHandler:           notifHandler,
		DashHandler:            dashHandler,
		AdminHandler:           adminHandler,
		HealthHandler:          healthHandler,
		CORSOrigins:            cfg.CORSOrigins,
		Phase2BorrowingEnabled: cfg.EnablePhase2Borrowing,
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

func validateMigrationMode(env string, autoMigrate bool) error {
	if isProductionEnvironment(env) && autoMigrate {
		return fmt.Errorf("AUTO_MIGRATE must be false in production; run /app/scc-migrate up before starting the API")
	}
	return nil
}

func isProductionEnvironment(env string) bool {
	return strings.EqualFold(strings.TrimSpace(env), "production")
}

type evidenceStorageInitializer interface {
	CreateBucketIfNotExists(context.Context) error
	Ready(context.Context) error
}

// initializeEvidenceStorage keeps administrative bucket provisioning out of
// the production API credential. deploy-vps.sh owns bucket creation and the
// private-policy enforcement with the MinIO root credential; the API's
// service account only proves that the already-provisioned bucket is ready.
func initializeEvidenceStorage(ctx context.Context, env string, store evidenceStorageInitializer) error {
	if isProductionEnvironment(env) {
		return store.Ready(ctx)
	}
	return store.CreateBucketIfNotExists(ctx)
}

type recurringCron interface {
	Start(context.Context, time.Duration)
}

func startCron(ctx context.Context, enabled bool, cron recurringCron, interval time.Duration) bool {
	if !enabled || cron == nil {
		return false
	}
	go cron.Start(ctx, interval)
	return true
}

func startScheduledJobs(
	ctx context.Context,
	runBackgroundJobs bool,
	phase2BorrowingEnabled bool,
	workOrderCron recurringCron,
	borrowCron recurringCron,
	interval time.Duration,
) (workOrderStarted, borrowStarted bool) {
	workOrderStarted = startCron(ctx, runBackgroundJobs, workOrderCron, interval)
	borrowStarted = startCron(ctx, runBackgroundJobs && phase2BorrowingEnabled, borrowCron, interval)
	return workOrderStarted, borrowStarted
}
