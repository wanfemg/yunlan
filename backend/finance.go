package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	financeBasePath    = "/finance"
	financeProxyHeader = "X-Siteadmin-Finance"
)

func validFinanceProxySecret(value string) bool {
	if len(value) < 32 || len(value) > 128 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func parseFinanceUpstream(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	invalid := errors.New("FINANCE_UPSTREAM must be an HTTP loopback IP with an explicit port (for example http://127.0.0.1:8765)")
	target, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, "http://") || target.User != nil || target.Path != "" ||
		target.ForceQuery || target.RawQuery != "" || strings.Contains(raw, "#") {
		return nil, invalid
	}
	host, rawPort, err := net.SplitHostPort(target.Host)
	if err != nil {
		return nil, invalid
	}
	ip, err := netip.ParseAddr(host)
	port, portErr := strconv.Atoi(rawPort)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || port < 1 || port > 65535 {
		return nil, invalid
	}
	return target, nil
}

func registerFinanceRoutes(router *gin.Engine, upstream *url.URL, proxySecret string, settings ...Config) {
	adminToken := ""
	if len(settings) > 0 {
		registerFinanceSourceRoute(router, settings[0], proxySecret)
		adminToken = settings[0].AdminToken
	}
	requireOwner := requireAdminToken(adminToken)
	financeAccess := func(c *gin.Context) {
		if c.Param("financePath") == "/healthz" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
			c.Next()
			return
		}
		if !c.GetBool("siteadmin.owner_authenticated") &&
			!(adminToken != "" && adminTokenValidator(adminToken)(c.GetHeader("Authorization"))) &&
			isFinancePageRequest(c.Request) {
			c.Redirect(http.StatusSeeOther, "/login?redirect=%2Fmoney")
			c.Abort()
			return
		}
		requireOwner(c)
	}
	router.Any(financeBasePath, financeAccess, func(c *gin.Context) {
		financeHeaders(c.Writer.Header())
		location := url.URL{Path: financeBasePath + "/", RawQuery: c.Request.URL.RawQuery, ForceQuery: c.Request.URL.ForceQuery}
		http.Redirect(c.Writer, c.Request, location.String(), http.StatusPermanentRedirect)
	})
	finance := newFinanceHandler(upstream, proxySecret)
	router.Any(financeBasePath+"/*financePath", financeAccess, func(c *gin.Context) {
		request := c.Request
		if upstream != nil {
			route := c.Param("financePath")
			switch route {
			case "/api/login", "/api/logout":
				financeHeaders(c.Writer.Header())
				c.JSON(http.StatusNotFound, gin.H{"error": "圈钱中心不需要登录"})
				return
			}
		}
		if request.Context().Done() == nil {
			// Gin exposes CloseNotify even if the underlying writer does not support it.
			// Give the proxy a cancellation context so it skips that deprecated interface.
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			request = request.WithContext(ctx)
		}
		finance.ServeHTTP(c.Writer, request)
	})
}

func newFinanceHandler(upstream *url.URL, proxySecret string) http.Handler {
	if upstream == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeFinanceUnavailable(w, r, http.StatusServiceUnavailable, "财务后台尚未配置。")
		})
	}
	return &httputil.ReverseProxy{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
			DisableCompression:    true,
		},
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			// The gateway does not parse these parameters. Preserve them for the finance service.
			request.Out.URL.RawQuery = request.In.URL.RawQuery
			request.Out.Header.Del("Authorization")
			request.Out.Header.Set(financeProxyHeader, proxySecret)
			request.Out.Header.Del("Forwarded")
			request.Out.Header.Del("X-Real-IP")
			for header := range request.Out.Header {
				if strings.HasPrefix(strings.ToLower(header), "x-forwarded-") {
					delete(request.Out.Header, header)
				}
			}
			request.Out.Header.Del("Cookie")
		},
		ModifyResponse: func(response *http.Response) error {
			financeHeaders(response.Header)
			response.Header.Del("Set-Cookie")
			if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
				// The site-admin shell embeds finance from the same origin.
				// Other origins remain blocked and all script restrictions stay intact.
				policies := response.Header.Values("Content-Security-Policy")
				if len(policies) > 0 {
					response.Header.Del("Content-Security-Policy")
					for _, policy := range policies {
						directives := strings.Split(policy, ";")
						for index, directive := range directives {
							fields := strings.Fields(directive)
							if len(fields) == 2 && strings.EqualFold(fields[0], "frame-ancestors") && fields[1] == "'none'" {
								directives[index] = " frame-ancestors 'self'"
							}
						}
						response.Header.Add("Content-Security-Policy", strings.Join(directives, ";"))
					}
				}
				response.Header.Set("X-Frame-Options", "SAMEORIGIN")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeFinanceUnavailable(w, r, http.StatusBadGateway, "暂时无法连接财务后台，请稍后重试。")
		},
	}
}

func financeHeaders(headers http.Header) {
	headers.Set("Cache-Control", "no-store")
	if headers.Get("X-Content-Type-Options") == "" {
		headers.Set("X-Content-Type-Options", "nosniff")
	}
	if headers.Get("Referrer-Policy") == "" {
		headers.Set("Referrer-Policy", "same-origin")
	}
}

func isFinancePageRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	relative := strings.TrimPrefix(r.URL.Path, financeBasePath)
	if relative == "/" || relative == "/index.html" {
		return true
	}
	if relative == "/api" || relative == "/static" || relative == "/healthz" ||
		strings.HasPrefix(relative, "/api/") || strings.HasPrefix(relative, "/static/") {
		return false
	}
	return path.Ext(relative) == "" && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeFinanceUnavailable(w http.ResponseWriter, r *http.Request, status int, message string) {
	financeHeaders(w.Header())
	if isFinancePageRequest(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>财务后台暂不可用</title>
<style>body{margin:0;background:#f5f7fb;color:#243247;font-family:system-ui,sans-serif}main{max-width:32rem;margin:12vh auto;padding:2rem}h1{font-size:1.65rem}p{line-height:1.6}a{color:#2454bc}</style>
<main><h1>财务后台暂不可用</h1><p>%s</p><a href="/">返回站长后台</a></main>
</html>`, message)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
}
