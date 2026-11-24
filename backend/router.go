package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func newRouter(config Config, service *dashboardService) *gin.Engine {
	if service.network == nil {
		service.network = newNetworkStore(service)
	}
	if service.userManagement == nil {
		service.userManagement = newUserManagementState()
	}
	if service.risk == nil {
		service.risk = newRiskControlStore(service)
	}
	if service.ownerAuth == nil {
		service.ownerAuth = newOwnerAuth(service)
	}
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.Use(gin.Recovery())
	router.Use(ownerAuthMiddleware(service))
	router.Use(func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if requestPath != financeBasePath && !strings.HasPrefix(requestPath, financeBasePath+"/") {
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Referrer-Policy", "same-origin")
		}
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	router.GET("/api/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/api/meta", func(c *gin.Context) {
		c.JSON(http.StatusOK, SystemMeta{
			Mode: config.Mode, AuthRequired: true, QuotaPerUnit: config.QuotaPerUnit,
			Timezone: config.Timezone, FinanceConfigured: config.FinanceUpstream != nil,
			ProbeConfigured:            config.ProbeSharedSecret != "",
			NewAPIManagementConfigured: config.NewAPIUpstream != nil && config.NewAPIAdminToken != "" && config.NewAPIAdminUserID > 0,
		})
	})
	registerOwnerAuthRoutes(router, service)
	router.GET("/api/admin-session", requireAdminToken(config.AdminToken), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"authed": true})
	})
	cache := NewDashboardCache(30*time.Second, service.Dashboard)
	router.GET("/api/dashboard", requireAdminToken(config.AdminToken), func(c *gin.Context) {
		period := Period(c.DefaultQuery("period", string(Period24Hours)))
		if _, err := periodDuration(period); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errInvalidPeriod.Error()})
			return
		}
		data, err := cache.Get(c.Request.Context(), period)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dashboard data is temporarily unavailable"})
			return
		}
		c.JSON(http.StatusOK, data)
	})
	router.GET("/api/log-analysis", requireAdminToken(config.AdminToken), service.logAnalysisHTTP)
	router.GET("/api/log-analysis/models", requireAdminToken(config.AdminToken), service.modelLogAnalysisHTTP)
	router.GET("/api/task-logs", requireAdminToken(config.AdminToken), service.taskLogsHTTP)
	router.GET("/api/task-logs/:id/logs", requireAdminToken(config.AdminToken), service.taskRelatedLogsHTTP)
	registerTokenManagementRoutes(router, service, config.AdminToken)
	registerAutoGroupRoutes(router, service, config.AdminToken)
	registerRedemptionRoutes(router, service, config.AdminToken)
	registerUserManagementRoutes(router, service, config.AdminToken)
	registerIPAnalysisRoutes(router, service, config.AdminToken)
	registerRiskControlRoutes(router, service, config.AdminToken)
	registerCheckinRoutes(router, service, config.AdminToken)
	registerSystemConnectionRoutes(router, service, config.AdminToken)
	registerProbeIngest(router, service, config.ProbeSharedSecret)
	registerFinanceRoutes(router, config.FinanceUpstream, config.FinanceProxySecret, config)
	router.NoRoute(serveFrontend(config.StaticDir))
	return router
}

// This endpoint accepts server-signed auth observations, never browser claims.
func registerProbeIngest(router *gin.Engine, service *dashboardService, secret string) {
	router.POST("/internal/probe/events", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if secret == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "login probe is not configured"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid probe payload"})
			return
		}
		stamp := c.GetHeader("X-Probe-Timestamp")
		at, err := strconv.ParseInt(stamp, 10, 64)
		now := time.Now().Unix()
		if service.now != nil {
			now = service.now().Unix()
		}
		if err != nil || at < now-300 || at > now+300 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "expired probe signature"})
			return
		}
		signature, err := hex.DecodeString(c.GetHeader("X-Probe-Signature"))
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(stamp + "\n" + c.Request.URL.Path + "\n"))
		_, _ = mac.Write(body)
		if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid probe signature"})
			return
		}
		var event networkEvent
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid probe event"})
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF ||
			(event.Source != "login" && event.Source != "registration") ||
			event.TokenID != 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auth event source or timestamp"})
			return
		}
		if _, err := normalizeNetworkEvent(event); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auth event fields"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
		defer cancel()
		digest := sha256.Sum256(body)
		bodyDigest := hex.EncodeToString(digest[:])
		existing, found, err := service.network.FindEvent(ctx, event.Source, event.ID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "probe event lookup is unavailable"})
			return
		}
		if found {
			if existing.Metadata["probe_body_digest"] != bodyDigest {
				c.JSON(http.StatusConflict, gin.H{"error": "probe event id already has different content"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"success": true, "event_id": event.ID, "replayed": true})
			return
		}
		if event.OccurredAt < now-7*86400 || event.OccurredAt > now+300 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth observation is outside the accepted time window"})
			return
		}
		if event.Metadata == nil {
			event.Metadata = make(map[string]string)
		}
		event.Metadata["probe_body_digest"] = bodyDigest
		event, err = service.correlateProbeRegistration(ctx, event)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "registration correlation is temporarily unavailable"})
			return
		}
		if event.Source == "login" && event.Result == "success" && event.UserID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "completed login requires a server-verified user id"})
			return
		}
		if _, err := normalizeNetworkEvent(event); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth observation exceeds storage field limits"})
			return
		}
		if err := service.network.Add(ctx, event); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth observation could not be stored"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "event_id": event.ID})
	})
}

func requireAdminToken(expected string) gin.HandlerFunc {
	valid := adminTokenValidator(expected)
	return func(c *gin.Context) {
		if c.GetBool("siteadmin.owner_authenticated") || c.Request.Context().Value(internalTaskAuthKey{}) == true ||
			expected != "" && valid(c.GetHeader("Authorization")) {
			c.Next()
			return
		}
		c.Header("WWW-Authenticate", "Bearer")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "error": "请先使用站长账号登录。"})
	}
}

func adminTokenValidator(expected string) func(string) bool {
	wanted := sha256.Sum256([]byte(expected))
	return func(authorization string) bool {
		if expected == "" {
			return true
		}
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return false
		}
		provided := sha256.Sum256([]byte(parts[1]))
		return subtle.ConstantTimeCompare(wanted[:], provided[:]) == 1
	}
}

func serveFrontend(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "API endpoint not found"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if strings.Contains(requestPath, "\\") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		cleanPath := path.Clean("/" + requestPath)
		if cleanPath != "/" {
			relative, err := filepath.Localize(strings.TrimPrefix(cleanPath, "/"))
			if err != nil || !filepath.IsLocal(relative) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			if serveStaticFile(c, staticDir, relative) {
				return
			}
		}
		if path.Ext(cleanPath) != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
			return
		}
		c.Header("Cache-Control", "no-cache")
		if !serveStaticFile(c, staticDir, "index.html") {
			c.JSON(http.StatusNotFound, gin.H{"error": "frontend is not built"})
		}
	}
}

func serveStaticFile(c *gin.Context, staticDir, name string) bool {
	file, err := os.OpenInRoot(staticDir, name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	return true
}
