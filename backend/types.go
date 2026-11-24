package main

import "time"

type Period string

const (
	Period24Hours Period = "24h"
	Period7Days   Period = "7d"
	Period30Days  Period = "30d"
)

type UsageStats struct {
	Requests           *int64   `json:"requests"`
	SuccessfulRequests *int64   `json:"successful_requests"`
	FailedRequests     *int64   `json:"failed_requests"`
	SuccessRate        *float64 `json:"success_rate"`
	Quota              *int64   `json:"quota"`
	PromptTokens       *int64   `json:"prompt_tokens"`
	CompletionTokens   *int64   `json:"completion_tokens"`
	ActiveUsers        *int64   `json:"active_users"`
	AvgDurationSeconds *float64 `json:"avg_duration_seconds"`
}

type Inventory struct {
	TotalUsers     *int64 `json:"total_users"`
	TotalTokens    *int64 `json:"total_tokens"`
	ActiveTokens   *int64 `json:"active_tokens"`
	TotalChannels  *int64 `json:"total_channels"`
	ActiveChannels *int64 `json:"active_channels"`
	TotalModels    *int64 `json:"total_models"`
}

type TrendPoint struct {
	At       time.Time `json:"at"`
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
	Quota    int64     `json:"quota"`
}

type ModelUsage struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Quota    int64  `json:"quota"`
	Tokens   int64  `json:"tokens"`
}

type SourceStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type DashboardData struct {
	Mode          string         `json:"mode"`
	Period        Period         `json:"period"`
	Timezone      string         `json:"timezone"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Range         TimeRange      `json:"range"`
	QuotaPerUnit  float64        `json:"quota_per_unit"`
	Inventory     Inventory      `json:"inventory"`
	Usage         UsageStats     `json:"usage"`
	PreviousUsage UsageStats     `json:"previous_usage"`
	Trend         []TrendPoint   `json:"trend"`
	Models        []ModelUsage   `json:"models"`
	Sources       []SourceStatus `json:"sources"`
	Warnings      []string       `json:"warnings"`
}

type SystemMeta struct {
	Mode                       string  `json:"mode"`
	AuthRequired               bool    `json:"auth_required"`
	QuotaPerUnit               float64 `json:"quota_per_unit"`
	Timezone                   string  `json:"timezone"`
	FinanceConfigured          bool    `json:"finance_configured"`
	ProbeConfigured            bool    `json:"probe_configured"`
	NewAPIManagementConfigured bool    `json:"newapi_management_configured"`
}

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }
