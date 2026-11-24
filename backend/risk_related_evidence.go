package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// These rules enrich the manual review queue. They never grant automatic
// execution, which remains tied to the existing token/rate/baseline rule.
type riskReviewRules struct {
	RelatedAccountsEnabled    bool    `json:"related_accounts_enabled"`
	RelatedAccountMinUsers    int     `json:"related_account_min_users"`
	CallFailuresEnabled       bool    `json:"call_failures_enabled"`
	FailureRateThreshold      float64 `json:"failure_rate_threshold"`
	FailureRateIncrease       float64 `json:"failure_rate_increase"`
	QuotaSpikeEnabled         bool    `json:"quota_spike_enabled"`
	QuotaBurstRatio           float64 `json:"quota_burst_ratio"`
	QuotaMinPrevious          int64   `json:"quota_min_previous"`
	ModelFailuresEnabled      bool    `json:"model_failures_enabled"`
	ModelMinCalls             int     `json:"model_min_calls"`
	ModelFailureRateThreshold float64 `json:"model_failure_rate_threshold"`
}

func defaultRiskReviewRules() riskReviewRules {
	return riskReviewRules{
		RelatedAccountsEnabled: true, RelatedAccountMinUsers: 2,
		CallFailuresEnabled: true, FailureRateThreshold: .5, FailureRateIncrease: .25,
		QuotaSpikeEnabled: true, QuotaBurstRatio: 3, QuotaMinPrevious: 500000,
		ModelFailuresEnabled: true, ModelMinCalls: 20, ModelFailureRateThreshold: .6,
	}
}

func validateRiskReviewRules(rules riskReviewRules) error {
	for _, value := range []float64{rules.FailureRateThreshold, rules.FailureRateIncrease, rules.ModelFailureRateThreshold} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 1 {
			return riskFailure(http.StatusBadRequest, "复核规则的失败比例和增幅必须大于 0 且不超过 1。")
		}
	}
	if rules.RelatedAccountMinUsers < 2 || rules.RelatedAccountMinUsers > 1000 ||
		rules.QuotaBurstRatio < 1.5 || rules.QuotaBurstRatio > 1000 || math.IsNaN(rules.QuotaBurstRatio) || math.IsInf(rules.QuotaBurstRatio, 0) ||
		rules.QuotaMinPrevious < 1 || rules.QuotaMinPrevious > 1000000000000000 ||
		rules.ModelMinCalls < 2 || rules.ModelMinCalls > 10000000 {
		return riskFailure(http.StatusBadRequest, "关联账号、额度消耗或模型样本复核阈值无效。")
	}
	return nil
}

type riskRelatedEvidence struct {
	Type        string  `json:"type"`
	Source      string  `json:"source"`
	Strength    string  `json:"strength"`
	Key         string  `json:"key"`
	UserIDs     []int64 `json:"user_ids"`
	TotalUsers  int     `json:"total_users"`
	Truncated   bool    `json:"truncated"`
	FirstSeen   int64   `json:"first_seen"`
	LastSeen    int64   `json:"last_seen"`
	Description string  `json:"description"`
}

type riskRelatedGroup struct {
	kind, key       string
	users           map[int64]struct{}
	firstSeen, last int64
}

type riskRelatedIndex struct {
	byUser map[int64][]*riskRelatedGroup
}

// Build once per scan window using all users' events, then reuse it for every
// account. The assessment output is bounded; the underlying counts stay exact.
func newRiskRelatedIndex(events []networkEvent, start, end int64) *riskRelatedIndex {
	groups := map[string]*riskRelatedGroup{}
	for _, event := range events {
		if event.UserID <= 0 || event.OccurredAt < start || event.OccurredAt >= end {
			continue
		}
		kind, key := "", ""
		switch event.Source {
		case networkSourceLogin, networkSourceRegistration:
			if event.Result == "success" && event.BrowserID != "" {
				kind, key = "shared_verified_browser_id", event.BrowserID
			}
		case networkSourceCall:
			if event.TokenID > 0 {
				kind, key = "shared_token", strconv.FormatInt(event.TokenID, 10)
			}
		}
		if key == "" {
			continue
		}
		identity := kind + ":" + key
		group := groups[identity]
		if group == nil {
			digest := sha256.Sum256([]byte(identity))
			group = &riskRelatedGroup{kind: kind, key: hex.EncodeToString(digest[:8]),
				users: map[int64]struct{}{}, firstSeen: event.OccurredAt}
			groups[identity] = group
		}
		group.users[event.UserID] = struct{}{}
		group.firstSeen = min(group.firstSeen, event.OccurredAt)
		group.last = max(group.last, event.OccurredAt)
	}
	index := &riskRelatedIndex{byUser: map[int64][]*riskRelatedGroup{}}
	for _, group := range groups {
		if len(group.users) < 2 {
			continue
		}
		for id := range group.users {
			index.byUser[id] = append(index.byUser[id], group)
		}
	}
	for _, values := range index.byUser {
		sort.Slice(values, func(i, j int) bool {
			if len(values[i].users) != len(values[j].users) {
				return len(values[i].users) > len(values[j].users)
			}
			if values[i].kind != values[j].kind {
				return values[i].kind < values[j].kind
			}
			return values[i].key < values[j].key
		})
	}
	return index
}

func (snapshot *riskEventSnapshot) relatedIndex(start, end int64) *riskRelatedIndex {
	key := fmt.Sprintf("%d:%d", start, end)
	if snapshot.related == nil {
		snapshot.related = map[string]*riskRelatedIndex{}
	}
	if snapshot.related[key] == nil {
		snapshot.related[key] = newRiskRelatedIndex(snapshot.events, start, end)
	}
	return snapshot.related[key]
}

func appendRiskRelatedEvidence(assessment *riskAssessment, config riskConfig, index *riskRelatedIndex) {
	const groupLimit, memberLimit = 10, 20
	assessment.RelatedEvidence = []riskRelatedEvidence{}
	groups := index.byUser[assessment.UserID]
	hits := map[string]bool{}
	for position, group := range groups {
		// Inspect every group before bounding its presentation so larger or later
		// evidence cannot disappear from the deterministic rule result.
		if config.ReviewRules.RelatedAccountsEnabled && len(group.users) >= config.ReviewRules.RelatedAccountMinUsers {
			hits[group.kind] = true
		}
		if position >= groupLimit {
			continue
		}
		ids := make([]int64, 0, len(group.users))
		for id := range group.users {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) > memberLimit {
			// Always retain the account being assessed in the bounded member list.
			ids = ids[:memberLimit]
			present := false
			for _, id := range ids {
				present = present || id == assessment.UserID
			}
			if !present {
				ids[len(ids)-1] = assessment.UserID
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			}
		}
		item := riskRelatedEvidence{
			Type: group.kind, Key: group.key, UserIDs: ids, TotalUsers: len(group.users),
			Truncated: len(group.users) > memberLimit, FirstSeen: group.firstSeen, LastSeen: group.last,
			Source: "login_registration", Strength: "high",
			Description: "同一已验证 BrowserID 成功登录或注册多个账号；仅说明浏览器关联，不证明同一自然人。",
		}
		if group.kind == "shared_token" {
			item.Source, item.Strength = networkSourceCall, "medium"
			item.Description = "调用日志中的同一令牌关联多个账号，可能来自转发或共享服务，需人工复核。"
		}
		assessment.RelatedEvidence = append(assessment.RelatedEvidence, item)
	}
	assessment.RelatedEvidenceTruncated = len(groups) > groupLimit
	if assessment.RelatedEvidenceTruncated {
		assessment.DataNotes = append(assessment.DataNotes, "关联证据组超过展示上限，已按关联人数排序展示前 10 组；规则仍检查全部组。")
	}
	if hits["shared_verified_browser_id"] {
		assessment.Findings = append(assessment.Findings, riskFinding{
			ID: "shared_verified_browser_accounts", Source: "login_registration", Score: 45,
			Description: "成功认证的同一浏览器标识关联账号数达到阈值；存在共享浏览器等解释，仅进入人工复核。",
		})
	}
	if hits["shared_token"] {
		assessment.Findings = append(assessment.Findings, riskFinding{
			ID: "shared_token_accounts", Source: networkSourceCall, Score: 30,
			Description: "同一令牌在分析窗口关联多个账号，达到复核阈值；共享网关或转发服务需人工核实。",
		})
	}
	finalizeRiskAssessment(assessment)
}

type riskModelFailure struct {
	Model               string  `json:"model"`
	Calls               int     `json:"calls"`
	Failures            int     `json:"failures"`
	PreviousCalls       int     `json:"previous_calls"`
	PreviousFailures    int     `json:"previous_failures"`
	FailureRate         float64 `json:"failure_rate"`
	PreviousFailureRate float64 `json:"previous_failure_rate"`
}

func appendRiskCallReviewFindings(assessment *riskAssessment, config riskConfig, events []networkEvent) {
	rules, f := config.ReviewRules, &assessment.Features
	byModel := map[string]*riskModelFailure{}
	currentUnknown, previousUnknown := 0, 0
	for _, event := range events {
		if event.UserID != assessment.UserID || event.Source != networkSourceCall ||
			event.OccurredAt < assessment.WindowStart-(assessment.WindowEnd-assessment.WindowStart) ||
			event.OccurredAt >= assessment.WindowEnd {
			continue
		}
		current := event.OccurredAt >= assessment.WindowStart
		known := event.Result == "success" || event.Result == "failure"
		if !known {
			if current {
				currentUnknown++
			} else {
				previousUnknown++
			}
			continue
		}
		if current {
			f.KnownCallOutcomes++
		} else {
			f.PreviousKnownCallOutcomes++
			if event.Result == "failure" {
				f.PreviousFailedCalls++
			}
		}
		model := strings.TrimSpace(event.Metadata["model"])
		if model == "" {
			continue
		}
		if len(model) > 200 {
			model = string([]rune(model)[:min(200, utf8.RuneCountInString(model))])
		}
		stats := byModel[model]
		if stats == nil {
			stats = &riskModelFailure{Model: model}
			byModel[model] = stats
		}
		if current {
			stats.Calls++
			if event.Result == "failure" {
				stats.Failures++
			}
		} else {
			stats.PreviousCalls++
			if event.Result == "failure" {
				stats.PreviousFailures++
			}
		}
	}
	if f.KnownCallOutcomes > 0 {
		f.FailureRate = float64(f.FailedCalls) / float64(f.KnownCallOutcomes)
	}
	if f.PreviousKnownCallOutcomes > 0 {
		f.PreviousFailureRate = float64(f.PreviousFailedCalls) / float64(f.PreviousKnownCallOutcomes)
	}
	if currentUnknown+previousUnknown > 0 {
		assessment.DataNotes = append(assessment.DataNotes, fmt.Sprintf("本窗口有 %d 次、上一窗口有 %d 次调用结果未知，已排除失败率分母。", currentUnknown, previousUnknown))
	}
	f.FailureBaselineAvailable = f.PreviousKnownCallOutcomes >= config.MinRequests
	if rules.CallFailuresEnabled {
		if !f.FailureBaselineAvailable {
			assessment.DataNotes = append(assessment.DataNotes, "上一窗口已知调用结果不足，失败率异常未建立比较基线。")
		} else if f.KnownCallOutcomes >= config.MinRequests &&
			f.FailureRate >= rules.FailureRateThreshold &&
			f.FailureRate-f.PreviousFailureRate >= rules.FailureRateIncrease {
			assessment.Findings = append(assessment.Findings, riskFinding{ID: "call_failure_rate_spike", Source: networkSourceCall, Score: 30,
				Description: fmt.Sprintf("失败率由 %.1f%% 升至 %.1f%%，样本达到阈值；可能是上游、模型或客户端故障，必须人工判断原因。", f.PreviousFailureRate*100, f.FailureRate*100)})
		}
	}
	if f.PreviousQuota > 0 {
		f.QuotaBurstRatio = float64(f.CurrentQuota) / float64(f.PreviousQuota)
	}
	if rules.QuotaSpikeEnabled {
		if !f.BaselineAvailable || f.PreviousQuota < rules.QuotaMinPrevious {
			assessment.DataNotes = append(assessment.DataNotes, "上一窗口调用或额度样本不足，额度突增规则不作异常判断。")
		} else if f.Calls >= config.MinRequests && f.QuotaBurstRatio >= rules.QuotaBurstRatio {
			assessment.Findings = append(assessment.Findings, riskFinding{ID: "quota_consumption_spike", Source: networkSourceCall, Score: 35,
				Description: fmt.Sprintf("额度消耗相对上一等长窗口增加至 %.2f 倍；模型价格变化和正常业务增长需要人工排除。", f.QuotaBurstRatio)})
		}
	}
	modelHit := false
	missingModelBaseline := false
	models := make([]riskModelFailure, 0, len(byModel))
	for _, stats := range byModel {
		if stats.Calls == 0 {
			continue
		}
		stats.FailureRate = float64(stats.Failures) / float64(stats.Calls)
		if stats.PreviousCalls > 0 {
			stats.PreviousFailureRate = float64(stats.PreviousFailures) / float64(stats.PreviousCalls)
		}
		if rules.ModelFailuresEnabled && stats.Calls >= rules.ModelMinCalls {
			if stats.PreviousCalls < rules.ModelMinCalls {
				missingModelBaseline = true
			} else if stats.FailureRate >= rules.ModelFailureRateThreshold &&
				stats.FailureRate-stats.PreviousFailureRate >= rules.FailureRateIncrease {
				modelHit = true
			}
		}
		models = append(models, *stats)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Failures != models[j].Failures {
			return models[i].Failures > models[j].Failures
		}
		return models[i].Model < models[j].Model
	})
	assessment.ModelFailuresTruncated = len(models) > 20
	f.ModelFailures = models[:min(20, len(models))]
	if modelHit {
		assessment.Findings = append(assessment.Findings, riskFinding{ID: "model_failure_concentration", Source: networkSourceCall, Score: 30,
			Description: "至少一个模型的失败率较上一窗口明显升高，且两窗口模型样本充足；这是调用质量复核线索，不直接证明账号滥用。"})
	}
	if missingModelBaseline {
		assessment.DataNotes = append(assessment.DataNotes, "部分模型缺少足够上一窗口样本，未对这些模型作集中失败判断。")
	}
}

func finalizeRiskAssessment(assessment *riskAssessment) {
	assessment.Score = 0
	for _, finding := range assessment.Findings {
		assessment.Score += finding.Score
	}
	assessment.Score = min(100, assessment.Score)
	assessment.Level, assessment.ProposedAction = "low", "observe"
	if assessment.Score >= 70 {
		assessment.Level, assessment.ProposedAction = "high", "review"
	} else if assessment.Score >= 30 {
		assessment.Level, assessment.ProposedAction = "medium", "review"
	}
	if assessment.AutoEligible {
		assessment.ProposedAction = "ban"
	}
	if assessment.Whitelisted {
		assessment.AutoEligible, assessment.ProposedAction = false, "observe"
	}
}

type riskReviewRecord struct {
	ID                string `json:"id"`
	UserID            int64  `json:"user_id"`
	AssessmentID      string `json:"assessment_id"`
	Status            string `json:"status"`
	Reason            string `json:"reason"`
	Operator          string `json:"operator"`
	RuleVersion       int64  `json:"rule_version"`
	DetectionRevision int    `json:"detection_revision"`
	CreatedAt         int64  `json:"created_at"`
}

func (store *riskControlStore) userReview(ctx context.Context, id int64) (*riskReviewRecord, error) {
	var record riskReviewRecord
	found, err := store.get(ctx, "review:user:"+strconv.FormatInt(id, 10), &record)
	if err != nil || !found {
		return nil, err
	}
	return &record, nil
}

func (s *dashboardService) riskUserReviewHTTP(c *gin.Context) {
	id, err := riskUserID(c.Param("id"))
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	if _, err := s.riskModule().user(c.Request.Context(), id); err != nil {
		riskHTTPError(c, err)
		return
	}
	review, err := s.riskModule().userReview(c.Request.Context(), id)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": review})
}

func (s *dashboardService) riskSaveUserReviewHTTP(c *gin.Context) {
	id, err := riskUserID(c.Param("id"))
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	var input struct {
		AssessmentID string `json:"assessment_id"`
		Status       string `json:"status"`
		Reason       string `json:"reason"`
	}
	if err := decodeRiskJSON(c, &input); err != nil {
		riskHTTPError(c, err)
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if (input.Status != "pending" && input.Status != "confirmed" && input.Status != "false_positive" && input.Status != "watching") ||
		utf8.RuneCountInString(input.Reason) < 2 || utf8.RuneCountInString(input.Reason) > 1000 ||
		len(input.AssessmentID) == 0 || len(input.AssessmentID) > 200 {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "复核状态无效；请选择已保存的账号评估，并填写 2 至 1000 字理由。"))
		return
	}
	store := s.riskModule()
	owner, recordKey := riskID("review"), "review:user:"+strconv.FormatInt(id, 10)
	// Serialize feedback with account actions, so a saved false-positive review
	// cannot race an automatic action using the same assessment.
	leaseKey := "user:" + strconv.FormatInt(id, 10)
	acquired, err := store.acquire(c.Request.Context(), leaseKey, owner, 30)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	if !acquired {
		riskHTTPError(c, riskFailure(http.StatusConflict, "该账号的复核结果正在更新，稍后重试。"))
		return
	}
	defer store.release(context.Background(), leaseKey, owner)
	if _, err := store.user(c.Request.Context(), id); err != nil {
		riskHTTPError(c, err)
		return
	}
	var assessment riskAssessment
	found, err := store.get(c.Request.Context(), "assessment:"+input.AssessmentID, &assessment)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	if !found || assessment.UserID != id {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "账号评估不存在或不属于当前用户。"))
		return
	}
	previous, err := store.userReview(c.Request.Context(), id)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	record := riskReviewRecord{ID: riskID("review"), UserID: id, AssessmentID: assessment.ID,
		Status: input.Status, Reason: input.Reason, Operator: "admin",
		RuleVersion: assessment.RuleVersion, DetectionRevision: assessment.DetectionRevision, CreatedAt: store.now().Unix()}
	assessment.Review = &record
	auditID := riskID("audit")
	records := map[string]any{
		recordKey: record, "review:record:" + record.ID: record,
		"assessment:" + assessment.ID: assessment,
		"audit:" + auditID: map[string]any{
			"id": auditID, "event": "manual_review_saved", "user_id": id,
			"assessment_id": assessment.ID, "state": record.Status, "previous_review": previous,
			"reason": record.Reason, "operator": record.Operator, "created_at": record.CreatedAt,
			"rule_version": record.RuleVersion, "detection_revision": record.DetectionRevision,
		},
	}
	if err := store.putMany(c.Request.Context(), records); err != nil {
		riskHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": record})
}

// Read-only analyses show the most recent review as context. An assessment's
// own review is preserved on the assessment even after later reviews exist.
func (s *dashboardService) attachRiskReview(ctx context.Context, assessment *riskAssessment) error {
	review, err := s.riskModule().userReview(ctx, assessment.UserID)
	if err != nil {
		return err
	}
	assessment.LatestReview = review
	return nil
}

// Keep history listing bounded and avoid dropping the distinction between the
// reviewed assessment and later evidence for the same account.
func riskAssessmentReviewRejected(payload json.RawMessage) bool {
	var record struct {
		ID     string            `json:"id"`
		Review *riskReviewRecord `json:"review"`
	}
	return json.Unmarshal(payload, &record) == nil && record.Review != nil &&
		record.Review.AssessmentID == record.ID && record.Review.Status == "false_positive"
}
