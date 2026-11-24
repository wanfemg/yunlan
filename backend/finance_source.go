package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

// Finance inherits the already configured main database over a private
// machine-authenticated channel. This is never part of the browser-facing API.
func registerFinanceSourceRoute(router *gin.Engine, config Config, secret string) {
	router.GET("/internal/finance/source", func(c *gin.Context) {
		financeHeaders(c.Writer.Header())
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		ip, parseErr := netip.ParseAddr(host)
		values := c.Request.Header.Values(financeProxyHeader)
		if err != nil || parseErr != nil || !ip.IsLoopback() || ip.Zone() != "" ||
			!validFinanceProxySecret(secret) || len(values) != 1 ||
			subtle.ConstantTimeCompare([]byte(values[0]), []byte(secret)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "finance source is unavailable"})
			return
		}
		// Demo can never expose live connection values, even when a shell
		// accidentally inherited them from a production session.
		if config.Mode != "live" || !validDriver(config.MainDriver) ||
			strings.TrimSpace(config.MainDSN) == "" {
			c.JSON(http.StatusConflict, gin.H{"error": "finance source requires live site-admin configuration"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"mode": "live", "source": "siteadmin-main",
			"db_type": config.MainDriver, "db_dsn": config.MainDSN,
		})
	})
}
