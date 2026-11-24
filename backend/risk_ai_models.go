package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	riskAIModelsBodyLimit = 2 * 1024 * 1024
	riskAIModelsLimit     = 5000
)

type riskAIDraftRequest struct {
	BaseURL        *string `json:"base_url"`
	APIKey         string  `json:"api_key"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
	Model          *string `json:"model"`
}

type riskAIModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// Store and compare the actual API prefix rather than the input spelling.
// Root URLs and URLs ending in /v1 refer to the same upstream credential scope.
func normalizeRiskAIBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2000 {
		return "", riskFailure(http.StatusBadRequest, "请填写有效的 AI API 地址。")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", riskFailure(http.StatusBadRequest, "AI API 地址须为 HTTP(S) 地址，且不能包含账号密码、查询参数或片段。")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	escapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	// Accept a pasted compatible operation URL as well as the normal API base.
	for _, suffix := range []string{"/chat/completions", "/models"} {
		if strings.HasSuffix(escapedPath, suffix) {
			escapedPath = strings.TrimSuffix(escapedPath, suffix)
			break
		}
	}
	if !strings.HasSuffix(escapedPath, "/v1") {
		escapedPath += "/v1"
	}
	// Retain escaped path boundaries so two different proxy mounts cannot
	// accidentally share the same saved credential scope.
	parsed.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return "", riskFailure(http.StatusBadRequest, "AI API 地址的路径编码无效。")
	}
	parsed.RawPath = escapedPath
	return parsed.String(), nil
}

func sameRiskAIBase(left, right string) bool {
	leftBase, leftErr := normalizeRiskAIBase(left)
	rightBase, rightErr := normalizeRiskAIBase(right)
	return leftErr == nil && rightErr == nil && leftBase == rightBase
}

// An empty key means reuse only at the already-saved API prefix. Never attach
// the saved credential to a newly typed destination.
func (s *dashboardService) resolveRiskAIDraft(ctx context.Context, request riskAIDraftRequest, needModel bool) (riskAIConfig, error) {
	saved, err := s.riskModule().config(ctx)
	if err != nil {
		return riskAIConfig{}, err
	}
	config := saved.AI
	if request.BaseURL != nil {
		config.BaseURL = strings.TrimSpace(*request.BaseURL)
	}
	base, err := normalizeRiskAIBase(config.BaseURL)
	if err != nil {
		return riskAIConfig{}, err
	}
	if request.APIKey != "" {
		config.APIKey = strings.TrimSpace(request.APIKey)
	} else if saved.AI.APIKey != "" && !sameRiskAIBase(config.BaseURL, saved.AI.BaseURL) {
		return riskAIConfig{}, riskFailure(http.StatusBadRequest, "API 地址已变化，请填写对应的新密钥；已保存密钥不会发送到新地址。")
	}
	config.BaseURL = base
	if config.APIKey == "" {
		return riskAIConfig{}, riskFailure(http.StatusBadRequest, "请先填写 AI API 密钥。")
	}
	if len(config.APIKey) > 4096 || strings.ContainsAny(config.APIKey, "\r\n") {
		return riskAIConfig{}, riskFailure(http.StatusBadRequest, "AI API 密钥格式无效。")
	}
	if request.TimeoutSeconds != nil {
		config.TimeoutSeconds = *request.TimeoutSeconds
	}
	if config.TimeoutSeconds < 1 || config.TimeoutSeconds > 60 {
		return riskAIConfig{}, riskFailure(http.StatusBadRequest, "请求超时须为 1–60 秒。")
	}
	if request.Model != nil {
		config.Model = strings.TrimSpace(*request.Model)
	}
	if needModel && (config.Model == "" || !utf8.ValidString(config.Model) || len(config.Model) > 200) {
		return riskAIConfig{}, riskFailure(http.StatusBadRequest, "请填写或选择有效的 AI 模型，模型名称最多 200 字节。")
	}
	return config, nil
}

func riskAIUpstreamError(err error) error {
	var netError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netError) && netError.Timeout()) {
		return riskFailure(http.StatusGatewayTimeout, "AI 接口请求超时，请检查地址或调高超时时间。")
	}
	return riskFailure(http.StatusBadGateway, "AI 接口连接失败，请检查 API 地址及网络连接。")
}

func requestRiskAIModels(ctx context.Context, config riskAIConfig) ([]riskAIModel, string, error) {
	endpoint := config.BaseURL + "/models"
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, endpoint, riskFailure(http.StatusBadRequest, "模型列表请求地址无效。")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
	client := &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, endpoint, riskAIUpstreamError(err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型服务拒绝了密钥（401），请检查密钥是否有效。")
	case http.StatusForbidden:
		return nil, endpoint, riskFailure(http.StatusBadGateway, "密钥没有读取模型列表的权限（403）。")
	case http.StatusTooManyRequests:
		return nil, endpoint, riskFailure(http.StatusTooManyRequests, "模型服务请求过于频繁（429），请稍后再拉取。")
	case http.StatusNotFound:
		return nil, endpoint, riskFailure(http.StatusBadGateway, "该地址未提供兼容的 /v1/models 接口（404），请检查 API 地址或手动填写模型。")
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型列表地址发生跳转，已停止请求；请填写最终 API 地址。")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型服务返回异常状态，请检查 API 地址及服务状态。")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, riskAIModelsBodyLimit+1))
	if err != nil {
		return nil, endpoint, riskAIUpstreamError(err)
	}
	if len(body) > riskAIModelsBodyLimit {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型列表超过 2 MiB，未加载；请缩小上游列表或手动填写模型。")
	}
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Success *bool           `json:"success"`
		HasMore bool            `json:"has_more"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Data) == 0 ||
		string(envelope.Data) == "null" || (envelope.Success != nil && !*envelope.Success) ||
		(len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型接口未返回有效的兼容 JSON 模型列表，请检查 API 地址或手动填写模型。")
	}
	var rawModels []riskAIModel
	if json.Unmarshal(envelope.Data, &rawModels) != nil {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型列表 data 字段格式无效，请手动填写模型或检查上游接口。")
	}
	if len(rawModels) > riskAIModelsLimit || envelope.HasMore {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "模型列表超过 5000 项或上游仅返回了分页片段，未作为完整列表加载；请手动填写模型。")
	}
	models := make([]riskAIModel, 0, len(rawModels))
	seen := make(map[string]bool, len(rawModels))
	for _, model := range rawModels {
		// Keep exact provider IDs. Names containing whitespace are legitimate,
		// but control characters must never become options or request headers.
		if model.ID == "" || !utf8.ValidString(model.ID) || len(model.ID) > 200 ||
			strings.IndexFunc(model.ID, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			continue
		}
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if len(model.OwnedBy) > 200 {
			model.OwnedBy = ""
		}
		models = append(models, model)
	}
	if len(rawModels) > 0 && len(models) == 0 {
		return nil, endpoint, riskFailure(http.StatusBadGateway, "上游没有返回可用的模型 ID，请手动填写模型。")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, endpoint, nil
}

func (s *dashboardService) riskAIModelsHTTP(c *gin.Context) {
	var request riskAIDraftRequest
	if err := decodeRiskJSON(c, &request); err != nil {
		riskHTTPError(c, err)
		return
	}
	config, err := s.resolveRiskAIDraft(c.Request.Context(), request, false)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	models, endpoint, err := requestRiskAIModels(c.Request.Context(), config)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"models": models, "total": len(models), "endpoint": endpoint,
	}})
}

// A body-less request keeps the old saved-config validation behavior. A JSON
// body validates the current form without saving it or starting a risk scan.
func decodeRiskAITestDraft(c *gin.Context) (riskAIDraftRequest, error) {
	var request riskAIDraftRequest
	if c.Request.Body == nil {
		return request, nil
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&request)
	if err != nil && err != io.EOF {
		return request, riskFailure(http.StatusBadRequest, "AI 验证参数须为有效的 JSON 对象。")
	}
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		return request, riskFailure(http.StatusBadRequest, "AI 验证请求只能包含一个 JSON 对象。")
	}
	return request, nil
}
