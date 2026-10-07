package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/autoalbum"
	"pal-next-gallery-server/app/batch"
	store "pal-next-gallery-server/app/db"
	"pal-next-gallery-server/app/frontend"
	"pal-next-gallery-server/app/gallery"
	"pal-next-gallery-server/app/nasimport"
	"pal-next-gallery-server/app/operations"
	"pal-next-gallery-server/app/processing"
	"pal-next-gallery-server/app/publicshare"
	setupapp "pal-next-gallery-server/app/setup"
	"pal-next-gallery-server/app/transfer"
	"pal-next-gallery-server/app/trash"
	uploader "pal-next-gallery-server/app/uploader"
	userapp "pal-next-gallery-server/app/user"

	gin "github.com/gin-gonic/gin"
	env "github.com/pal-paul/go-libraries/pkg/env"
)

var (
	envVar Environment
	router *gin.Engine
)

type Environment struct {
	Mode string `env:"ENV_GIN_MODE,default=release"`
	Port string `env:"ENV_PORT,default=8081"`

	DatabaseURL     string `env:"ENV_DATABASE_URL"`
	AdminUsername   string `env:"ENV_ADMIN_USERNAME"`
	AdminPassword   string `env:"ENV_ADMIN_PASSWORD"`
	AdminConfig     string `env:"ENV_ADMIN_CONFIG,default=YES"`
	WebDir          string `env:"ENV_WEB_DIR,default=./web/dist"`
	MediaDir        string `env:"ENV_MEDIA_DIR,default=./vol/medias"`
	TmpDir          string `env:"ENV_TMP_DIR,default=./vol/tmp"`
	MaxUploadSize   int64  `env:"ENV_MAX_UPLOAD_SIZE_BYTES,default=107374182400"`
	MaxPendingSize  int64  `env:"ENV_MAX_PENDING_UPLOAD_BYTES_PER_USER,default=107374182400"`
	MaxStorageSize  int64  `env:"ENV_MAX_STORAGE_BYTES_PER_USER,default=1099511627776"`
	MinDiskFree     int64  `env:"ENV_MIN_DISK_FREE_BYTES,default=2147483648"`
	MaxActive       int    `env:"ENV_MAX_ACTIVE_UPLOADS_PER_USER,default=10"`
	HTTPReadTimeout string `env:"ENV_HTTP_READ_TIMEOUT,default=5m"`
	CleanupInterval string `env:"ENV_CLEANUP_INTERVAL,default=1h"`
	UploadRetention string `env:"ENV_UPLOAD_RETENTION,default=168h"`
	AllowedOrigins  string `env:"ENV_CORS_ALLOWED_ORIGINS,default=http://localhost:3000"`
	TrustedProxies  string `env:"ENV_TRUSTED_PROXIES"`
	Issuer          string `env:"ENV_ISSUER,default=issuer.palpaul.com"`
	AutoAlbumRunAt  string `env:"ENV_AUTO_ALBUM_RUN_AT,default=02:00"`
	AutoAlbumZone   string `env:"ENV_AUTO_ALBUM_TIMEZONE,default=Local"`
	NASImportPath   string `env:"NAS_IMPORT_PATH"`
}

// Initializing environment variables
func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	_, err := env.Unmarshal(&envVar)
	if err != nil {
		slog.Error("load environment", "error", err)
		os.Exit(1)
	}
}

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if strings.TrimSpace(envVar.DatabaseURL) == "" {
		return fmt.Errorf("ENV_DATABASE_URL is required")
	}
	readTimeout, err := time.ParseDuration(envVar.HTTPReadTimeout)
	if err != nil || readTimeout <= 0 {
		return fmt.Errorf("ENV_HTTP_READ_TIMEOUT must be a positive duration")
	}
	cleanupInterval, err := time.ParseDuration(envVar.CleanupInterval)
	if err != nil || cleanupInterval <= 0 {
		return fmt.Errorf("ENV_CLEANUP_INTERVAL must be a positive duration")
	}
	uploadRetention, err := time.ParseDuration(envVar.UploadRetention)
	if err != nil || uploadRetention <= 0 {
		return fmt.Errorf("ENV_UPLOAD_RETENTION must be a positive duration")
	}
	trustedProxies, err := parseTrustedProxies(envVar.TrustedProxies)
	if err != nil {
		return err
	}

	ctx := context.Background()
	database, err := store.NewPostgres(ctx, envVar.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}

	authService := auth.NewService(database, envVar.Issuer, auth.WithTrustedProxies(trustedProxies))
	adminConfigEnabled, err := parseYesNo(envVar.AdminConfig)
	if err != nil {
		return err
	}
	frontendService, err := frontend.New(envVar.WebDir)
	if err != nil {
		return err
	}
	setupService, err := setupapp.NewService(database, envVar.AdminUsername, envVar.AdminPassword, true)
	if err != nil {
		return fmt.Errorf("create setup service: %w", err)
	}
	autoAlbumSchedule, err := autoalbum.NewSchedule(envVar.AutoAlbumRunAt, envVar.AutoAlbumZone)
	if err != nil {
		return err
	}
	autoAlbumService := autoalbum.NewService(database)
	var nasImportService *nasimport.Service
	if strings.TrimSpace(envVar.NASImportPath) != "" {
		nasImportService, err = nasimport.New(database, envVar.NASImportPath, envVar.MediaDir)
		if err != nil {
			return fmt.Errorf("create NAS import service: %w", err)
		}
	}
	uploaderService, err := uploader.New(
		envVar.TmpDir,
		envVar.MediaDir,
		uploader.WithMaxUploadSize(envVar.MaxUploadSize),
		uploader.WithMaxPendingUploadSize(envVar.MaxPendingSize),
		uploader.WithMaxUserStorageSize(envVar.MaxStorageSize),
		uploader.WithMinDiskFree(envVar.MinDiskFree),
		uploader.WithMaxActiveUploads(envVar.MaxActive),
		uploader.WithMediaRepository(database),
	)
	if err != nil {
		return fmt.Errorf("create uploader service: %w", err)
	}
	userService := userapp.NewService(database, envVar.Issuer, envVar.MediaDir)
	galleryService := gallery.NewService(database, envVar.MediaDir)
	operationsService, err := operations.New(database, uploaderService, envVar.MediaDir, envVar.TmpDir, envVar.MinDiskFree)
	if err != nil {
		return fmt.Errorf("create operations service: %w", err)
	}
	processingService := processing.New(database, envVar.MediaDir)
	publicShareService := publicshare.New(database, envVar.MediaDir)
	trashService := trash.New(database, envVar.MediaDir)
	batchService := batch.New(database)
	transferService := transfer.New(database)

	// Setup router
	gin.SetMode(envVar.Mode)
	router = gin.New()
	proxyStrings := make([]string, 0, len(trustedProxies))
	for _, prefix := range trustedProxies {
		proxyStrings = append(proxyStrings, prefix.String())
	}
	if err := router.SetTrustedProxies(proxyStrings); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	router.Use(gin.Recovery(), requestLoggerMiddleware())
	router.Use(securityHeadersMiddleware())
	router.Use(corsMiddleware(envVar.AllowedOrigins))
	frontendService.RegisterRoutes(router)
	router.GET("/healthz", operationsService.Liveness)
	router.GET("/readyz", operationsService.Readiness)
	setupService.RegisterRoutes(router)
	router.POST("/auth/login", gin.WrapF(authService.Login))
	router.POST("/auth/verify", gin.WrapF(authService.VerifyTOTP))
	router.POST("/auth/recovery", gin.WrapF(authService.VerifyRecoveryCodeHandler))
	router.GET("/public/:token", publicShareService.Resolve)
	router.GET("/public/:token/download", publicShareService.Download)

	protected := router.Group("/")
	protected.Use(authService.RequireAuth())
	protected.GET("/auth/session", gin.WrapF(authService.Session))
	protected.POST("/auth/logout", gin.WrapF(authService.LogoutHandler))
	protected.POST("/auth/recovery-codes", authService.RecoveryCodes)

	adminRoutes := protected.Group("/admin")
	adminRoutes.Use(userService.RequireRole("admin"))
	if adminConfigEnabled {
		adminRoutes.GET("/config", userService.ConfigurationPage)
	}
	adminRoutes.GET("/users", userService.ListUsers)
	adminRoutes.POST("/users", userService.CreateUser)
	adminRoutes.PATCH("/users/:id/password", userService.ResetUserPassword)
	adminRoutes.PATCH("/users/:id/quota", userService.SetUserQuota)
	adminRoutes.PATCH("/users/:id/trash-retention", userService.SetUserTrashRetention)
	adminRoutes.GET("/storage/dashboard", userService.StorageDashboard)
	adminRoutes.GET("/operations/integrity", operationsService.Integrity)
	adminRoutes.GET("/processing-jobs", processingService.ListJobs)
	adminRoutes.POST("/processing-jobs/:id/retry", processingService.RetryJob)
	adminRoutes.POST("/trash/cleanup", trashService.CleanupHandler)

	protected.GET("/media/files/:id/download", userService.DownloadMedia)
	protected.GET("/media/files/:id/thumbnail", userService.DownloadThumbnail)
	gallery.RegisterRoutes(protected, galleryService)

	userRoutes := protected.Group("/")
	userRoutes.Use(userService.RequireRole("user"))
	userRoutes.PUT("/users/me/folder", userService.SetMyFolder)
	userRoutes.GET("/media/app", userService.MediaPage)
	userRoutes.POST("/media/public-links", publicShareService.Create)
	userRoutes.DELETE("/media/public-links/:id", publicShareService.Delete)
	userRoutes.POST("/media/upload-batches", batchService.Create)
	userRoutes.GET("/media/upload-batches/:id", batchService.Get)
	userRoutes.POST("/media/upload-batches/:id/cancel", batchService.Cancel)
	userRoutes.GET("/media/export", transferService.Export)
	userRoutes.POST("/media/import", transferService.Import)
	userRoutes.GET("/media/files/:id/processing-status", processingService.Status)
	userRoutes.POST("/media/files/:id/shares", userService.ShareMedia)
	userRoutes.DELETE("/media/files/:id/shares", userService.UnshareMedia)
	if err := uploader.RegisterRoutes(userRoutes, uploaderService); err != nil {
		return fmt.Errorf("register uploader routes: %w", err)
	}

	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	defer stopScheduler()
	go processingService.Run(schedulerCtx)
	go trashService.Run(schedulerCtx)
	if nasImportService != nil {
		go nasImportService.RunScheduled(schedulerCtx, autoAlbumSchedule, func(err error) {
			slog.Error("NAS media import failed", "error", err)
		})
		slog.Info("NAS media import scheduled", "path", envVar.NASImportPath, "runAt", envVar.AutoAlbumRunAt, "timezone", autoAlbumSchedule.Location.String())
	}
	go autoAlbumService.RunScheduled(schedulerCtx, autoAlbumSchedule, func(err error) {
		slog.Error("automatic album reconciliation failed", "error", err)
	})
	slog.Info("automatic albums scheduled", "runAt", envVar.AutoAlbumRunAt, "timezone", autoAlbumSchedule.Location.String())
	go runCleanup(schedulerCtx, operationsService, cleanupInterval, uploadRetention)
	go reportStartupIntegrity(schedulerCtx, operationsService)

	// Graceful shutdown
	srv := &http.Server{
		Addr:              ":" + envVar.Port,
		Handler:           router,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Channel to listen for interrupt signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serverErrors := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- fmt.Errorf("failed to start server: %w", err)
		}
	}()

	slog.Info("server started", "port", envVar.Port)

	select {
	case err := <-serverErrors:
		return err
	case <-quit:
	}
	stopScheduler()
	slog.Info("shutting down server")

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancel()

	if shutdownErr != nil {
		return fmt.Errorf("server forced to shutdown: %w", shutdownErr)
	}

	slog.Info("server stopped gracefully")
	return nil
}

func reportStartupIntegrity(ctx context.Context, service *operations.Service) {
	report, err := service.ScanIntegrity(ctx)
	if err != nil {
		slog.Error("startup integrity scan failed", "error", err)
		return
	}
	if len(report.FilesWithoutRecords) > 0 || len(report.RecordsWithoutFiles) > 0 {
		slog.Warn("startup integrity issues found",
			"filesWithoutRecords", len(report.FilesWithoutRecords),
			"recordsWithoutFiles", len(report.RecordsWithoutFiles))
		return
	}
	slog.Info("startup integrity scan completed")
}

func runCleanup(ctx context.Context, service *operations.Service, interval, uploadRetention time.Duration) {
	run := func() {
		result, err := service.Cleanup(ctx, time.Now(), uploadRetention)
		if err != nil {
			slog.Error("scheduled cleanup failed", "error", err)
			return
		}
		if result.AuthRecords > 0 || result.Uploads > 0 {
			slog.Info("scheduled cleanup completed", "authRecords", result.AuthRecords, "uploads", result.Uploads)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func parseYesNo(value string) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "YES":
		return true, nil
	case "NO":
		return false, nil
	default:
		return false, fmt.Errorf("ENV_ADMIN_CONFIG must be YES or NO")
	}
}

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			address, addressErr := netip.ParseAddr(item)
			if addressErr != nil {
				return nil, fmt.Errorf("ENV_TRUSTED_PROXIES contains invalid address %q", item)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func securityHeadersMiddleware() gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Header("X-Content-Type-Options", "nosniff")
		context.Header("X-Frame-Options", "DENY")
		context.Header("Referrer-Policy", "no-referrer")
		context.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		context.Next()
	}
}

func requestLoggerMiddleware() gin.HandlerFunc {
	return func(context *gin.Context) {
		started := time.Now()
		context.Next()
		slog.Info("http request",
			"method", context.Request.Method,
			"path", context.Request.URL.Path,
			"status", context.Writer.Status(),
			"durationMs", time.Since(started).Milliseconds(),
			"clientIP", context.ClientIP(),
			"errors", len(context.Errors),
		)
	}
}

// corsMiddleware handles CORS
func corsMiddleware(allowedOrigins string) gin.HandlerFunc {
	allowed := make(map[string]struct{})
	for _, origin := range strings.Split(allowedOrigins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		_, explicitlyAllowed := allowed[origin]
		nullSameOrigin := origin == "null" && c.GetHeader("Sec-Fetch-Site") == "same-origin"
		if origin != "" && !explicitlyAllowed && !nullSameOrigin && !isSameOrigin(origin, c.Request.Host) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if origin != "" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Vary", "Origin")
		}
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Authorization, Accept, Cache-Control,  X-Client-Id")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, DELETE, PUT, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func isSameOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == requestHost
}
