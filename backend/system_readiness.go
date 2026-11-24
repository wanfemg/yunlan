package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type systemReadinessCheck struct {
	Label string `json:"label"`
	Value string `json:"value"`
	State string `json:"state,omitempty"`
}

type systemReadinessModule struct {
	ID        string                 `json:"id"`
	State     string                 `json:"state"`
	Message   string                 `json:"message"`
	CheckedAt int64                  `json:"checked_at"`
	Checks    []systemReadinessCheck `json:"checks"`
	Warnings  []string               `json:"warnings"`
}

func newSystemReadinessModule(id string) systemReadinessModule {
	return systemReadinessModule{
		ID: id, State: "missing", CheckedAt: time.Now().Unix(),
		Checks: []systemReadinessCheck{}, Warnings: []string{},
	}
}

func readinessCheck(label string, value bool, yes, no string) systemReadinessCheck {
	item := systemReadinessCheck{Label: label, Value: no, State: "configured"}
	if value {
		item.Value, item.State = yes, "ready"
	}
	return item
}

func readinessTime(value int64) string {
	if value <= 0 || value > time.Now().Add(time.Minute).Unix() {
		return "暂无记录"
	}
	return time.Unix(value, 0).UTC().Format(time.RFC3339)
}

// Machine requests are made only from the explicit readiness handler. Decode
// into private DTOs and expose a separate fixed projection, never raw responses.
func readMachineReadiness(ctx context.Context, target, header, secret string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("machine readiness request is invalid")
	}
	request.Header.Set(header, secret)
	request.Header.Set("Accept", "application/json")
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("machine readiness service is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("machine readiness service did not accept the request")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, out) != nil {
		return errors.New("machine readiness response is invalid")
	}
	return nil
}

func (s *dashboardService) financeSystemReadiness(ctx context.Context) systemReadinessModule {
	item := newSystemReadinessModule("finance")
	item.Message = "尚未配置圈钱中心机器服务。"
	if s.config.FinanceUpstream == nil || s.config.FinanceProxySecret == "" {
		return item
	}
	// Revalidate the loopback target at use, even for manually constructed Config.
	target, err := parseFinanceUpstream(s.config.FinanceUpstream.String())
	if err != nil || target == nil {
		item.State, item.Message = "configured", "圈钱中心机器服务地址配置无效。"
		return item
	}
	var source struct {
		Mode        string `json:"mode"`
		Integrated  bool   `json:"integrated"`
		SourceReady bool   `json:"source_ready"`
		Ready       bool   `json:"ready"`
		Poller      struct {
			Running  bool  `json:"running"`
			LastRun  int64 `json:"last_run"`
			HasError bool  `json:"has_error"`
		} `json:"poller"`
		ReporterRunning bool `json:"reporter_running"`
		Notifications   struct {
			Configured          bool  `json:"configured"`
			SignatureConfigured bool  `json:"signature_configured"`
			AutomaticEnabled    bool  `json:"automatic_enabled"`
			DailyEnabled        bool  `json:"daily_enabled"`
			WeeklyEnabled       bool  `json:"weekly_enabled"`
			ExternalDelivery    bool  `json:"external_delivery"`
			DeliveryVerified    bool  `json:"delivery_verified"`
			LastSuccess         int64 `json:"last_success"`
			LastErrorAt         int64 `json:"last_error_at"`
		} `json:"notifications"`
	}
	target.Path = financeBasePath + "/api/readiness"
	if readMachineReadiness(ctx, target.String(), financeProxyHeader, s.config.FinanceProxySecret, &source) != nil {
		item.State, item.Message = "unreachable", "圈钱中心未返回有效机器就绪状态，请检查进程、机器密钥与本机端口。"
		return item
	}
	if source.Mode != "live" && source.Mode != "demo" {
		item.State, item.Message = "unreachable", "圈钱中心返回的运行模式无效，未确认真实来源。"
		return item
	}
	item.State, item.Message = "configured", "圈钱中心机器服务可达，仍有来源或后台任务需要处理。"
	item.Checks = append(item.Checks,
		readinessCheck("机器服务", source.Integrated, "已通过机器认证", "集成状态未确认"),
		readinessCheck("业务来源", source.SourceReady && source.Ready, "只读连接检查通过", "来源未就绪"),
		readinessCheck("订单轮询", source.Poller.Running, "轮询线程运行中", "轮询线程未运行"),
		systemReadinessCheck{Label: "最近轮询", Value: readinessTime(source.Poller.LastRun)},
		readinessCheck("报告任务", source.ReporterRunning, "报告线程运行中", "报告线程未运行"),
		readinessCheck("钉钉通知", source.Notifications.Configured, "Webhook 已配置", "Webhook 未配置"),
		readinessCheck("钉钉加签", source.Notifications.SignatureConfigured, "已配置", "未配置"),
		readinessCheck("自动订单通知", source.Notifications.AutomaticEnabled, "已开启", "已关闭"),
		readinessCheck("日报 / 周报", source.Notifications.DailyEnabled || source.Notifications.WeeklyEnabled, "至少一项已开启", "均未开启"),
	)
	if source.Poller.HasError {
		item.Warnings = append(item.Warnings, "轮询记录存在错误，在圈钱中心查看处理状态。")
	}
	if source.Mode == "demo" || s.config.Mode == "demo" {
		item.State, item.Message = "demo", "圈钱中心处于演示链路，当前检查不代表真实财务数据已接通。"
		for index := range item.Checks {
			if item.Checks[index].State == "ready" {
				item.Checks[index].State = "demo"
			}
			if item.Checks[index].Label == "业务来源" {
				item.Checks[index].Value = "演示来源，不确认真实业务连接"
			}
		}
		item.Checks = append(item.Checks, systemReadinessCheck{Label: "钉钉投递", Value: "演示不确认真实投递", State: "demo"})
		if source.Notifications.ExternalDelivery {
			item.Warnings = append(item.Warnings, "主后台当前为演示模式，财务服务却允许外发；请核对两侧启动模式。")
		}
		return item
	}
	if source.Integrated && source.SourceReady && source.Ready && source.Poller.Running && !source.Poller.HasError {
		item.State, item.Message = "ready", "真实财务来源检查通过，机器服务与订单轮询已就绪。"
	}
	verified := source.Notifications.Configured && source.Notifications.ExternalDelivery &&
		source.Notifications.DeliveryVerified && source.Notifications.LastSuccess > 0 &&
		source.Notifications.LastSuccess <= time.Now().Add(time.Minute).Unix() &&
		source.Notifications.LastSuccess >= source.Notifications.LastErrorAt
	item.Checks = append(item.Checks,
		readinessCheck("钉钉真实投递", verified, "当前配置有成功回执", "尚未验证或最近投递失败"),
		systemReadinessCheck{Label: "最近成功投递", Value: readinessTime(source.Notifications.LastSuccess)},
	)
	if source.Notifications.LastErrorAt > 0 {
		item.Checks = append(item.Checks, systemReadinessCheck{Label: "最近失败时间", Value: readinessTime(source.Notifications.LastErrorAt)})
	}
	item.Warnings = append(item.Warnings, "本次就绪检查不会发送通知；钉钉验证以当前配置的真实投递记录为准。")
	return item
}

func (s *dashboardService) probeSystemReadiness(ctx context.Context) systemReadinessModule {
	item := newSystemReadinessModule("probe")
	item.Message = "尚未配置登录注册探针共享机器密钥。"
	item.Warnings = append(item.Warnings, "机器服务就绪不代表网关路由已切换；真实登录或注册认证事件是否进入后台仍需确认。")
	if s.config.ProbeSharedSecret == "" {
		return item
	}
	var source struct {
		Service           string `json:"service"`
		Configured        bool   `json:"configured"`
		Ready             bool   `json:"ready"`
		UpstreamReady     bool   `json:"upstream_ready"`
		CollectorReady    bool   `json:"collector_ready"`
		SpoolReady        bool   `json:"spool_ready"`
		PendingEvents     int    `json:"pending_events"`
		QuarantinedEvents int    `json:"quarantined_events"`
	}
	if readMachineReadiness(ctx, "http://127.0.0.1:18081/_siteadmin_probe/readyz",
		"X-Siteadmin-Probe", s.config.ProbeSharedSecret, &source) != nil || source.Service != "login-probe" {
		item.State, item.Message = "unreachable", "本机探针未返回有效机器就绪状态，请检查进程、共享密钥与端口。"
		return item
	}
	item.State, item.Message = "configured", "探针机器服务可达，仍有上游、采集端或本地队列未就绪。"
	if source.Configured && source.Ready && source.UpstreamReady && source.CollectorReady && source.SpoolReady {
		item.State, item.Message = "ready", "探针机器服务、上游和采集端检查通过；认证流量接入仍待确认。"
	}
	if s.config.Mode == "demo" {
		item.State, item.Message = "demo", "探针机器服务已响应，主后台当前仍使用演示数据。"
	}
	item.Checks = append(item.Checks,
		readinessCheck("NewAPI 上游", source.UpstreamReady, "公共状态可达", "公共状态未通过"),
		readinessCheck("后台采集端", source.CollectorReady, "健康检查通过", "健康检查未通过"),
		readinessCheck("本地队列", source.SpoolReady, "可读取", "不可读取"),
		systemReadinessCheck{Label: "待投递 / 隔离事件", Value: readinessCount(source.PendingEvents) + " / " + readinessCount(source.QuarantinedEvents)},
		systemReadinessCheck{Label: "网关与真实认证事件", Value: "待实际确认", State: "configured"},
	)
	if source.QuarantinedEvents > 0 {
		item.Warnings = append(item.Warnings, "存在隔离事件，需要在探针机器队列中检查原因。")
	}
	return item
}

func readinessCount(value int) string {
	if value < 0 {
		return "未知"
	}
	// Counts are derived from typed fields, not provider-controlled labels.
	return strconv.Itoa(value)
}

func (s *dashboardService) aiSystemReadiness(ctx context.Context) systemReadinessModule {
	item := newSystemReadinessModule("ai")
	item.Message = "尚未配置完整 AI 审核连接。"
	config, err := s.riskModule().config(ctx)
	if err != nil {
		item.State, item.Message = "unreachable", "AI 审核配置暂时不可读，未执行模型请求。"
		return item
	}
	configured := config.AI.BaseURL != "" && config.AI.APIKey != "" && config.AI.Model != ""
	if configured {
		item.State, item.Message = "configured", "AI 连接已配置，实际连接和结构化响应在风控中心验证。"
	}
	item.Checks = append(item.Checks,
		readinessCheck("连接字段", configured, "地址、密钥和模型已填写", "连接字段未完整填写"),
		readinessCheck("辅助审核", config.AI.Enabled, "已启用", "未启用"),
		systemReadinessCheck{Label: "真实模型验证", Value: "本检查不发起模型请求", State: "configured"},
	)
	return item
}

func (s *dashboardService) newAPISystemReadiness() systemReadinessModule {
	item := newSystemReadinessModule("newapi")
	item.Message = "尚未配置 NewAPI 管理桥接。"
	if s.config.NewAPIUpstream == nil || s.config.NewAPIAdminToken == "" || s.config.NewAPIAdminUserID <= 0 {
		return item
	}
	item.State, item.Message = "configured", "管理桥接已配置，当前运行连接尚无匹配的通过记录。"
	checked := false
	for _, check := range s.connectionChecks(s.config) {
		if check.ID != "newapi" {
			continue
		}
		checked = true
		item.Checks = append(item.Checks, systemReadinessCheck{Label: "最近身份验证", Value: readinessTime(check.CheckedAt)})
		if check.State == "connected" && s.config.Mode == "live" {
			item.State, item.Message = "ready", "当前连接有已保存的管理员身份验证记录；本次未访问用户接口。"
		} else if check.State == "failed" {
			item.State, item.Message = "unreachable", "当前连接最近一次验证未通过，在连接设置重新检查。"
		}
	}
	if !checked {
		item.Checks = append(item.Checks, systemReadinessCheck{Label: "管理员身份验证", Value: "尚无匹配验证记录", State: "configured"})
	}
	if s.config.Mode == "demo" {
		item.State, item.Message = "demo", "已填写管理桥接，主后台仍使用演示数据。"
	}
	item.Warnings = append(item.Warnings, "此项读取最近保存的验证记录，不会重新请求管理员或用户资料。")
	return item
}

func (s *dashboardService) ipSystemReadiness() systemReadinessModule {
	item := newSystemReadinessModule("ip")
	item.Message = "IP 情报模块尚未初始化。"
	if s.network == nil {
		return item
	}
	status, sync := s.network.ipIntelligenceStatus(), s.network.syncJobStatus()
	geo := status.Geo["city_available"] && status.Geo["asn_available"]
	item.State, item.Message = "configured", "本地 IP 情报模块已初始化，部分数据源或同步任务尚未就绪。"
	if status.Available && geo && sync.WorkerReady {
		item.State, item.Message = "ready", "本地归属地、ASN、有效情报数据与后台同步工作进程已就绪。"
	}
	if !status.Configured && !status.Available && !geo {
		item.State, item.Message = "missing", "尚未安装可用本地 IP 归属地或情报数据。"
	}
	if s.config.Mode == "demo" {
		item.State, item.Message = "demo", "本地情报状态已读取，当前业务事件使用演示样本。"
	}
	item.Checks = append(item.Checks,
		readinessCheck("归属地", status.Geo["city_available"], "本地数据库可用", "本地数据库不可用"),
		readinessCheck("ASN", status.Geo["asn_available"], "本地数据库可用", "本地数据库不可用"),
		readinessCheck("代理情报", status.Available, "存在未过期数据", "无可用未过期数据"),
		readinessCheck("IP 同步工作进程", sync.WorkerReady, "运行中", "未运行"),
		systemReadinessCheck{Label: "调用数据同步", Value: readinessSyncLabel(sync.State)},
	)
	if sync.State == "failed" || sync.State == "interrupted" {
		item.Warnings = append(item.Warnings, "调用同步尚未完成，在 IP 分析中查看任务并继续同步。")
	}
	item.Warnings = append(item.Warnings, "此项仅读取本地数据状态，不采集真实用户、不发起外部 IP 查询。")
	return item
}

func readinessSyncLabel(state string) string {
	switch state {
	case "completed":
		return "最近任务已完成"
	case "running":
		return "正在同步"
	case "queued":
		return "排队中"
	case "failed":
		return "失败，等待处理"
	case "interrupted":
		return "已中断，可继续"
	case "cancelled":
		return "已停止"
	default:
		return "尚未执行"
	}
}

func (s *dashboardService) systemReadinessHTTP(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	tasks := []func() systemReadinessModule{
		func() systemReadinessModule { return s.financeSystemReadiness(ctx) },
		func() systemReadinessModule { return s.probeSystemReadiness(ctx) },
		func() systemReadinessModule { return s.aiSystemReadiness(ctx) },
		func() systemReadinessModule { return s.newAPISystemReadiness() },
		func() systemReadinessModule { return s.ipSystemReadiness() },
	}
	type result struct {
		index int
		value systemReadinessModule
	}
	results := make(chan result, len(tasks))
	for index, task := range tasks {
		go func() { results <- result{index: index, value: task()} }()
	}
	modules := make([]systemReadinessModule, len(tasks))
	for index, id := range []string{"finance", "probe", "ai", "newapi", "ip"} {
		modules[index] = newSystemReadinessModule(id)
		modules[index].State, modules[index].Message = "unreachable", "本次检查超时，未确认模块就绪状态。"
	}
	for range tasks {
		select {
		case result := <-results:
			modules[result.index] = result.value
		case <-ctx.Done():
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
				"active_mode": s.config.Mode, "checked_at": time.Now().Unix(), "read_only": true, "modules": modules,
			}})
			return
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"active_mode": s.config.Mode, "checked_at": time.Now().Unix(), "read_only": true, "modules": modules,
	}})
}
