package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	maxRedemptionBatch        = 1000
	maxRedemptionQuota  int64 = 1<<53 - 1           // NewAPI's wallet quota limit.
	maxRedemptionExpiry       = int64(253402300799) // 9999-12-31T23:59:59Z.
	redemptionKeyLength       = 32
	redemptionAlphabet        = "abcdefghijklmnopqrstuvwxyz234567"
)

// Keep the generator request compatible with newapitools. Amounts are in the
// currency units configured by QUOTA_PER_UNIT, rather than raw NewAPI quota.
type redemptionGenerateParams struct {
	Name        string   `json:"name"`
	Count       int      `json:"count"`
	KeyPrefix   string   `json:"key_prefix"`
	QuotaMode   string   `json:"quota_mode"`
	FixedAmount *float64 `json:"fixed_amount"`
	MinAmount   *float64 `json:"min_amount"`
	MaxAmount   *float64 `json:"max_amount"`
	ExpireMode  string   `json:"expire_mode"`
	ExpireDays  *int     `json:"expire_days"`
	ExpireDate  *string  `json:"expire_date"`

	fixedAmountJSON string
	minAmountJSON   string
	maxAmountJSON   string
}

func (params *redemptionGenerateParams) UnmarshalJSON(data []byte) error {
	// Decode typed fields for the existing contract, but retain decimal input
	// before float64 rounding can change an amount at a quota boundary.
	type wireParams redemptionGenerateParams
	var decoded wireParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var amounts struct {
		Fixed json.RawMessage `json:"fixed_amount"`
		Min   json.RawMessage `json:"min_amount"`
		Max   json.RawMessage `json:"max_amount"`
	}
	if err := json.Unmarshal(data, &amounts); err != nil {
		return err
	}
	*params = redemptionGenerateParams(decoded)
	params.fixedAmountJSON, params.minAmountJSON, params.maxAmountJSON = string(amounts.Fixed), string(amounts.Min), string(amounts.Max)
	return nil
}

type redemptionGenerateResult struct {
	Keys        []string `json:"keys"`
	Count       int      `json:"count"`
	Quotas      []int64  `json:"quotas"` // Same order as keys, including random mode.
	ExpiredTime int64    `json:"expired_time"`
	Mode        string   `json:"mode"`
	Persisted   bool     `json:"persisted"`
}

type redemptionPlan struct {
	name        string
	count       int
	prefix      string
	minQuota    int64
	maxQuota    int64
	expiredTime int64
}

type redemptionInputError struct{ message string }

func (e *redemptionInputError) Error() string { return e.message }

func redemptionInvalid(message string) error {
	return &redemptionInputError{message: message}
}

func registerRedemptionRoutes(router *gin.Engine, service *dashboardService, adminToken string) {
	group := router.Group("/api/redemptions", requireAdminToken(adminToken))
	group.POST("/generate", service.redemptionGenerateHTTP)
	group.GET("", service.redemptionListHTTP)
	group.GET("/statistics", service.redemptionStatisticsHTTP)
	group.DELETE("/batch", service.redemptionBatchDeleteHTTP)
	group.POST("/batch", service.redemptionBatchDeleteHTTP)
	group.POST("/batch-delete", service.redemptionBatchDeleteHTTP)
	group.DELETE("/:id", service.redemptionDeleteHTTP)
	if service.config.Mode == "demo" {
		group.GET("/demo-sample", func(c *gin.Context) {
			minAmount, maxAmount := 1.0, 25.0
			days := 30
			sample, err := service.GenerateRedemptions(c.Request.Context(), redemptionGenerateParams{
				Name: "本地测试样例", Count: 30, KeyPrefix: "demo_",
				QuotaMode: "random", MinAmount: &minAmount, MaxAmount: &maxAmount,
				ExpireMode: "days", ExpireDays: &days,
			})
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "演示兑换码样例暂不可用"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"success": true, "data": sample})
		})
	}
}

func (s *dashboardService) redemptionGenerateHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	var params redemptionGenerateParams
	if err := decoder.Decode(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "兑换码生成参数格式无效"})
		return
	}
	// A request must contain exactly one JSON value; never ignore trailing data.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请求须包含一个 JSON 对象"})
		return
	}
	result, err := s.GenerateRedemptions(c.Request.Context(), params)
	if err != nil {
		var input *redemptionInputError
		if errors.As(err, &input) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": input.Error()})
			return
		}
		// Database errors can include the full code, SQL, or connection details.
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "兑换码生成暂不可用，请检查数据库连接和写入权限"})
		return
	}
	message := fmt.Sprintf("成功生成 %d 个兑换码", result.Count)
	if !result.Persisted {
		message = "演示兑换码已加入本地管理列表，未写入 NewAPI，不能用于兑换"
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": message, "data": result})
}

func (s *dashboardService) GenerateRedemptions(ctx context.Context, params redemptionGenerateParams) (redemptionGenerateResult, error) {
	if err := ctx.Err(); err != nil {
		return redemptionGenerateResult{}, err
	}
	now := s.now()
	plan, err := validateRedemptionParams(params, s.config.QuotaPerUnit, now, s.config.Location)
	if err != nil {
		return redemptionGenerateResult{}, err
	}
	if s.config.Mode != "demo" && s.management.db == nil {
		return redemptionGenerateResult{}, errors.New("redemption management database is not configured")
	}
	keys, err := generateRedemptionKeys(plan.count, plan.prefix)
	if err != nil {
		return redemptionGenerateResult{}, err
	}
	quotas := make([]int64, plan.count)
	for i := range quotas {
		quotas[i] = plan.minQuota
		if plan.minQuota != plan.maxQuota {
			value, err := rand.Int(rand.Reader, big.NewInt(plan.maxQuota-plan.minQuota+1))
			if err != nil {
				return redemptionGenerateResult{}, fmt.Errorf("generate random quota: %w", err)
			}
			quotas[i] += value.Int64()
		}
	}
	if err := ctx.Err(); err != nil {
		return redemptionGenerateResult{}, err
	}
	if s.config.Mode != "demo" {
		if err := s.insertRedemptionBatch(ctx, plan, keys, quotas, now.Unix()); err != nil {
			return redemptionGenerateResult{}, err
		}
	} else {
		s.saveDemoRedemptionBatch(plan, keys, quotas, now.Unix())
	}
	// Return usable codes only after the entire live batch has committed.
	return redemptionGenerateResult{
		Keys: keys, Count: len(keys), Quotas: quotas, ExpiredTime: plan.expiredTime,
		Mode: s.config.Mode, Persisted: s.config.Mode != "demo",
	}, nil
}

func validateRedemptionParams(params redemptionGenerateParams, quotaPerUnit float64, now time.Time, location *time.Location) (redemptionPlan, error) {
	plan := redemptionPlan{name: strings.TrimSpace(params.Name), count: params.Count, prefix: params.KeyPrefix}
	if !utf8.ValidString(plan.name) || utf8.RuneCountInString(plan.name) < 1 || utf8.RuneCountInString(plan.name) > 20 {
		return plan, redemptionInvalid("兑换码名称须为 1–20 个字符")
	}
	for _, char := range plan.name {
		if unicode.IsControl(char) {
			return plan, redemptionInvalid("兑换码名称不能包含控制字符")
		}
	}
	if plan.count < 1 || plan.count > maxRedemptionBatch {
		return plan, redemptionInvalid("每批兑换码数量须为 1–1000")
	}
	if !utf8.ValidString(plan.prefix) || len(plan.prefix) > 20 {
		return plan, redemptionInvalid("兑换码前缀不能超过 20 个字节")
	}
	for _, char := range plan.prefix {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return plan, redemptionInvalid("兑换码前缀不能包含空白或控制字符")
		}
	}
	if quotaPerUnit <= 0 || math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) {
		return plan, errors.New("invalid quota conversion configuration")
	}
	mode := params.QuotaMode
	if mode == "" {
		mode = "fixed"
	}
	var err error
	switch mode {
	case "fixed":
		plan.minQuota, err = redemptionAmountQuota(params.FixedAmount, params.fixedAmountJSON, quotaPerUnit)
		plan.maxQuota = plan.minQuota
	case "random":
		plan.minQuota, err = redemptionAmountQuota(params.MinAmount, params.minAmountJSON, quotaPerUnit)
		if err == nil {
			plan.maxQuota, err = redemptionAmountQuota(params.MaxAmount, params.maxAmountJSON, quotaPerUnit)
		}
		if err == nil {
			min, _ := redemptionDecimal(params.MinAmount, params.minAmountJSON)
			max, _ := redemptionDecimal(params.MaxAmount, params.maxAmountJSON)
			if min.Cmp(max) > 0 {
				err = redemptionInvalid("最小金额不能大于最大金额")
			}
		}
	default:
		err = redemptionInvalid("金额模式须为 fixed 或 random")
	}
	if err != nil {
		return plan, err
	}
	plan.expiredTime, err = redemptionExpiration(params, now, location)
	return plan, err
}

func redemptionDecimal(amount *float64, original string) (*big.Rat, error) {
	if amount == nil || *amount <= 0 || math.IsNaN(*amount) || math.IsInf(*amount, 0) {
		return nil, redemptionInvalid("金额须为大于 0 的有限数值")
	}
	if original == "" {
		original = strconv.FormatFloat(*amount, 'g', -1, 64)
	}
	value, ok := new(big.Rat).SetString(original)
	if !ok || value.Sign() <= 0 {
		return nil, redemptionInvalid("金额须为大于 0 的有限数值")
	}
	return value, nil
}

func redemptionAmountQuota(amount *float64, original string, quotaPerUnit float64) (int64, error) {
	value, err := redemptionDecimal(amount, original)
	if err != nil {
		return 0, err
	}
	unit, ok := new(big.Rat).SetString(strconv.FormatFloat(quotaPerUnit, 'g', -1, 64))
	if !ok || unit.Sign() <= 0 {
		return 0, errors.New("invalid quota conversion configuration")
	}
	quota := new(big.Rat).Mul(value, unit)
	var rounded, remainder big.Int
	rounded.QuoRem(quota.Num(), quota.Denom(), &remainder)
	if remainder.Lsh(&remainder, 1).Cmp(quota.Denom()) >= 0 {
		rounded.Add(&rounded, big.NewInt(1))
	}
	if rounded.Sign() <= 0 || rounded.Cmp(big.NewInt(maxRedemptionQuota)) > 0 {
		return 0, redemptionInvalid("金额换算后的额度须为 1–9007199254740991")
	}
	return rounded.Int64(), nil
}

func redemptionExpiration(params redemptionGenerateParams, now time.Time, location *time.Location) (int64, error) {
	switch params.ExpireMode {
	case "", "never":
		return 0, nil
	case "days":
		if params.ExpireDays == nil || *params.ExpireDays < 1 {
			return 0, redemptionInvalid("有效天数须为正整数")
		}
		const secondsPerDay int64 = 86400
		if now.Unix() >= maxRedemptionExpiry || int64(*params.ExpireDays) > (maxRedemptionExpiry-now.Unix())/secondsPerDay {
			return 0, redemptionInvalid("有效天数超出支持范围")
		}
		return now.Unix() + int64(*params.ExpireDays)*secondsPerDay, nil
	case "date":
		if params.ExpireDate == nil || *params.ExpireDate == "" {
			return 0, redemptionInvalid("须指定到期日期")
		}
		if location == nil {
			location = time.UTC
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
			at, err := time.ParseInLocation(layout, *params.ExpireDate, location)
			if err != nil {
				continue
			}
			if at.Unix() <= now.Unix() || at.Unix() > maxRedemptionExpiry {
				return 0, redemptionInvalid("到期时间须晚于当前时间且不超过 9999 年")
			}
			return at.Unix(), nil
		}
		return 0, redemptionInvalid("到期日期格式无效")
	default:
		return 0, redemptionInvalid("有效期模式须为 never、days 或 date")
	}
}

func generateRedemptionKeys(count int, prefix string) ([]string, error) {
	// Preserve the complete UTF-8 prefix and fill to 32 characters, matching
	// NewAPI's char(32) column without slicing through multibyte characters.
	suffixLength := redemptionKeyLength - utf8.RuneCountInString(prefix)
	if count < 1 || count > maxRedemptionBatch || !utf8.ValidString(prefix) || len(prefix) > 20 || suffixLength < 12 {
		return nil, errors.New("invalid redemption key parameters")
	}
	keys := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	random := make([]byte, suffixLength)
	for attempts := 0; len(keys) < count && attempts < count*3; attempts++ {
		if _, err := rand.Read(random); err != nil {
			return nil, fmt.Errorf("generate redemption key: %w", err)
		}
		for i := range random {
			// The 32-symbol alphabet divides the byte domain exactly.
			random[i] = redemptionAlphabet[random[i]&31]
		}
		key := prefix + string(random)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) != count {
		return nil, errors.New("could not generate enough unique redemption keys")
	}
	return keys, nil
}

func (s *dashboardService) insertRedemptionBatch(ctx context.Context, plan redemptionPlan, keys []string, quotas []int64, createdTime int64) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	database := s.management
	columns, err := inspectCheckinColumns(ctx, database, "redemptions",
		"user_id", "key", "status", "name", "quota", "created_time", "redeemed_time", "used_user_id")
	if err != nil {
		return err
	}
	if !columns["expired_time"] && plan.expiredTime != 0 {
		return redemptionInvalid("当前 NewAPI 缺少过期时间字段，无法生成带有效期的兑换码；请使用永不过期。")
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin redemption batch: %w", err)
	}
	defer tx.Rollback()
	// At most 9,000 parameters, below both drivers' prepared parameter limits.
	columnList := "user_id, " + tokenIdentifier(database.driver, "key") + ", status, name, quota, created_time, redeemed_time, used_user_id"
	row := "(?, ?, ?, ?, ?, ?, ?, ?)"
	if columns["expired_time"] {
		columnList += ", expired_time"
		row = "(?, ?, ?, ?, ?, ?, ?, ?, ?)"
	}
	query := "INSERT INTO redemptions (" + columnList + ") VALUES " + strings.TrimSuffix(strings.Repeat(row+",", len(keys)), ",")
	args := make([]any, 0, len(keys)*9)
	for i, key := range keys {
		// Preserve newapitools' creator ID 1; generated codes start enabled and unused.
		args = append(args, int64(1), key, int64(1), plan.name, quotas[i], createdTime, int64(0), int64(0))
		if columns["expired_time"] {
			args = append(args, plan.expiredTime)
		}
	}
	if _, err := tx.ExecContext(ctx, bindQuery(database.driver, query), args...); err != nil {
		return fmt.Errorf("insert redemption batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit redemption batch: %w", err)
	}
	return nil
}
