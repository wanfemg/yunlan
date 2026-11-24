package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type checkinReview struct {
	PaidStatus      string `json:"paid_status"`
	Funding         string `json:"funding"`
	HistoryVerified bool   `json:"history_verified"`
	Reason          string `json:"reason"`
	UpdatedAt       int64  `json:"updated_at"`
}

type checkinReviewJournal struct {
	Reviews map[int64]checkinReview `json:"reviews"`
	History []checkinReviewAudit    `json:"history"`
}

type checkinReviewAudit struct {
	UserID int64          `json:"user_id"`
	Before *checkinReview `json:"before"`
	After  checkinReview  `json:"after"`
}

func (s *dashboardService) readCheckinReviews() (map[int64]checkinReview, error) {
	module := s.checkinState()
	module.mu.Lock()
	defer module.mu.Unlock()
	if !module.loaded {
		module.reviews = map[int64]checkinReview{}
		if s.config.Mode != "demo" {
			payload, err := os.ReadFile(filepath.Join(s.config.DataDir, "checkin-reviews.json"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("本地运营标注暂不可用")
			}
			if len(payload) > 0 {
				var journal checkinReviewJournal
				if json.Unmarshal(payload, &journal) != nil || journal.Reviews == nil {
					return nil, errors.New("本地运营标注文件格式无效")
				}
				module.reviews = journal.Reviews
			}
		}
		module.loaded = true
	}
	result := make(map[int64]checkinReview, len(module.reviews))
	for id, review := range module.reviews {
		result[id] = review
	}
	return result, nil
}

func (s *dashboardService) saveCheckinReview(userID int64, value checkinReview) error {
	if _, err := s.readCheckinReviews(); err != nil {
		return err
	}
	module := s.checkinState()
	module.mu.Lock()
	defer module.mu.Unlock()
	next := make(map[int64]checkinReview, len(module.reviews)+1)
	for id, review := range module.reviews {
		next[id] = review
	}
	next[userID] = value
	if s.config.Mode != "demo" {
		journal := checkinReviewJournal{Reviews: next, History: []checkinReviewAudit{}}
		path := filepath.Join(s.config.DataDir, "checkin-reviews.json")
		if payload, err := os.ReadFile(path); err == nil {
			var previous checkinReviewJournal
			if json.Unmarshal(payload, &previous) != nil {
				return errors.New("本地运营标注文件格式无效")
			}
			journal.History = previous.History
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法读取本地运营标注")
		}
		var before *checkinReview
		if old, exists := module.reviews[userID]; exists {
			before = &old
		}
		journal.History = append(journal.History, checkinReviewAudit{UserID: userID, Before: before, After: value})
		if len(journal.History) > 20000 {
			journal.History = journal.History[len(journal.History)-20000:]
		}
		payload, err := json.Marshal(journal)
		if err != nil {
			return errors.New("无法编码本地运营标注")
		}
		if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
			return errors.New("无法创建本地标注目录")
		}
		file, err := os.CreateTemp(s.config.DataDir, ".checkin-reviews-*")
		if err != nil {
			return errors.New("无法保存本地运营标注")
		}
		temp := file.Name()
		defer os.Remove(temp)
		if _, err = file.Write(payload); err != nil {
			file.Close()
			return errors.New("无法写入本地运营标注")
		}
		if err = file.Sync(); err != nil {
			file.Close()
			return errors.New("无法同步本地运营标注")
		}
		if err = file.Close(); err != nil {
			return errors.New("无法完成本地运营标注")
		}
		if err = os.Rename(temp, path); err != nil {
			return errors.New("无法替换本地运营标注")
		}
	}
	module.reviews = next
	module.version++
	module.cache = map[string]checkinCacheEntry{}
	return nil
}

func (s *dashboardService) checkinReviewHTTP(c *gin.Context) {
	id, err := riskUserID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "账号 ID 无效"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		PaidStatus      string `json:"paid_status"`
		Funding         string `json:"funding"`
		HistoryVerified bool   `json:"history_verified"`
		Reason          string `json:"reason"`
	}
	if decoder.Decode(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "标注格式无效"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "标注须为单个 JSON 对象"})
		return
	}
	if input.PaidStatus != "unknown" && input.PaidStatus != "paid" && input.PaidStatus != "none" ||
		input.Funding != "unknown" && input.Funding != "free_only" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "付费或来源标注无效"})
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || !validLogFilter(input.Reason, 1000) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "请填写 1–1000 字的人工确认依据"})
		return
	}
	// Check account existence against the same primary source used by analysis.
	f, err := s.parseCheckinFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}
	users, _, err := s.readCheckinAccounts(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "账号数据暂不可用，标注未保存"})
		return
	}
	found := false
	for _, user := range users {
		if user.ID == id {
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "该账号不存在或已删除"})
		return
	}
	review := checkinReview{PaidStatus: input.PaidStatus, Funding: input.Funding,
		HistoryVerified: input.HistoryVerified, Reason: input.Reason, UpdatedAt: userClock(s).Unix()}
	if review.UpdatedAt <= 0 {
		review.UpdatedAt = time.Now().Unix()
	}
	if err := s.saveCheckinReview(id, review); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"review": review}})
}
