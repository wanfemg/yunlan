package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type networkAttributeAudit struct {
	ID       string         `json:"id"`
	IP       string         `json:"ip"`
	At       int64          `json:"at"`
	Reason   string         `json:"reason"`
	Operator string         `json:"operator"`
	Before   *networkIPInfo `json:"before"`
	After    networkIPInfo  `json:"after"`
}

func networkAttributeRevision(info *networkIPInfo) string {
	if info == nil {
		return ""
	}
	body, _ := json.Marshal(info)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}
func (n *networkStore) rawNetworkAttributes(ctx context.Context, ip string) (*networkIPInfo, error) {
	if err := n.ensure(ctx); err != nil {
		return nil, err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		value, exists := n.attrs[ip]
		n.mu.Unlock()
		if !exists {
			return nil, nil
		}
		return &value, nil
	}
	var body string
	err := n.service.management.queryRow(ctx, "SELECT payload FROM siteadmin_ip_enrichment WHERE ip=?", ip).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var info networkIPInfo
	if json.Unmarshal([]byte(body), &info) != nil {
		return nil, errors.New("invalid stored attributes")
	}
	return &info, nil
}
func (n *networkStore) attributeHistory(ctx context.Context, ip string) ([]networkAttributeAudit, error) {
	items := []networkAttributeAudit{}
	if n.service.config.Mode == "demo" {
		n.attributeMu.Lock()
		defer n.attributeMu.Unlock()
		for i := len(n.attributeAudit) - 1; i >= 0 && len(items) < 50; i-- {
			if n.attributeAudit[i].IP == ip {
				items = append(items, n.attributeAudit[i])
			}
		}
		return items, nil
	}
	rows, err := n.service.management.query(ctx, `SELECT payload FROM siteadmin_network_audit WHERE kind='ip_attribute' AND subject=? ORDER BY occurred_at DESC,id DESC LIMIT 50`, ip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var item networkAttributeAudit
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &item) != nil {
			return nil, errors.New("invalid attribute audit")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *dashboardService) networkAttributesReadHTTP(c *gin.Context) {
	ip, kind := classifyNetworkIP(c.Query("ip"))
	if kind != "public" {
		c.JSON(400, gin.H{"success": false, "error": "仅支持有效公网 IP 的属性修正。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	value, err := s.network.rawNetworkAttributes(ctx, ip)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "人工属性读取失败。"})
		return
	}
	history, err := s.network.attributeHistory(ctx, ip)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "属性变更记录读取失败。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"attributes": value, "revision": networkAttributeRevision(value), "audit": history}})
}
func (s *dashboardService) saveNetworkAttributesHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var request struct {
		networkIPInfo
		Reason   string  `json:"reason"`
		Expected *string `json:"expected_revision"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Expected == nil || strings.TrimSpace(request.Reason) == "" || !validLogFilter(request.Reason, 1000) {
		c.JSON(400, gin.H{"success": false, "error": "修正属性须填写原因，并提交读取时的属性版本。"})
		return
	}
	n := s.network
	info, err := normalizedNetworkAttributes(request.networkIPInfo, n.clock().Unix())
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": "请填写有效的公网 IP、来源及属性。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	n.attributeMu.Lock()
	locked := true
	defer func() {
		if locked {
			n.attributeMu.Unlock()
		}
	}()
	before, err := n.rawNetworkAttributes(ctx, info.IP)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "当前属性读取失败。"})
		return
	}
	if *request.Expected != networkAttributeRevision(before) {
		c.JSON(409, gin.H{"success": false, "error": "属性已被其他操作修改，请重新读取后再保存。"})
		return
	}
	audit := networkAttributeAudit{ID: fmt.Sprintf("ipattr-%d", time.Now().UnixNano()), IP: info.IP, At: n.clock().Unix(), Reason: strings.TrimSpace(request.Reason), Operator: "站长后台管理员", Before: before, After: info}
	if s.config.Mode == "demo" {
		n.mu.Lock()
		n.attrs[info.IP] = info
		n.mu.Unlock()
		n.attributeAudit = append(n.attributeAudit, audit)
	} else {
		db := s.management
		tx, err := db.db.BeginTx(ctx, nil)
		if err != nil {
			c.JSON(503, gin.H{"success": false, "error": "属性事务无法开始。"})
			return
		}
		defer tx.Rollback()
		payload, _ := json.Marshal(info)
		if before == nil {
			_, err = tx.ExecContext(ctx, bindQuery(db.driver, "INSERT INTO siteadmin_ip_enrichment (ip,payload) VALUES (?,?)"), info.IP, string(payload))
		} else {
			oldPayload, _ := json.Marshal(before)
			result, updateErr := tx.ExecContext(ctx, bindQuery(db.driver, "UPDATE siteadmin_ip_enrichment SET payload=? WHERE ip=? AND payload=?"), string(payload), info.IP, string(oldPayload))
			err = updateErr
			if err == nil {
				if affected, _ := result.RowsAffected(); affected != 1 {
					c.JSON(409, gin.H{"success": false, "error": "属性版本已变化，请重新读取。"})
					return
				}
			}
		}
		rawAudit, _ := json.Marshal(audit)
		if err == nil {
			_, err = tx.ExecContext(ctx, bindQuery(db.driver, "INSERT INTO siteadmin_network_audit (id,kind,subject,occurred_at,payload) VALUES (?,?,?,?,?)"), audit.ID, "ip_attribute", info.IP, audit.At, string(rawAudit))
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			c.JSON(409, gin.H{"success": false, "error": "保存未完成，属性可能已更新，请重新读取。"})
			return
		}
		n.mu.Lock()
		n.attrs[info.IP] = info
		n.attrsAt = time.Time{}
		n.mu.Unlock()
	}
	n.revision.Add(1)
	n.attributeMu.Unlock()
	locked = false
	history, err := n.attributeHistory(ctx, info.IP)
	if err != nil {
		history = []networkAttributeAudit{audit}
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"attributes": info, "revision": networkAttributeRevision(&info), "audit": history}})
}
func (s *dashboardService) networkExportHTTP(c *gin.Context) {
	source, start, end, err := networkHTTPWindow(c, s.network.clock())
	user, userErr := networkHTTPID(c.Query("user_id"))
	token, tokenErr := networkHTTPID(c.Query("token_id"))
	ip, ipErr := networkFilterAddress(c.Query("ip"), false)
	if err != nil || userErr != nil || tokenErr != nil || ipErr != nil || token > 0 && source != networkSourceCall {
		c.JSON(400, gin.H{"error": "导出筛选条件无效。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	data, err := s.network.cachedAnalysis(ctx, source, start, end, user, token, ip)
	if err != nil {
		c.JSON(503, gin.H{"error": "IP 导出暂时不可用。"})
		return
	}
	if len(data.IPs) > 100000 {
		c.JSON(400, gin.H{"error": "导出超过 100000 个 IP，请缩小时间或账号范围。"})
		return
	}
	var body bytes.Buffer
	body.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&body)
	_ = writer.Write([]string{"source", "window_start", "window_end", "ip", "events", "success", "failure", "accounts", "tokens", "first_seen", "last_seen", "country", "region", "city", "asn", "organization", "isp", "network_type", "vpn", "public_proxy", "residential_proxy", "tor", "data_source"})
	for _, item := range data.IPs {
		info := data.Enrichment[item.Key]
		record := []string{source, strconv.FormatInt(start, 10), strconv.FormatInt(end, 10), item.Key, strconv.FormatInt(item.Events, 10), strconv.FormatInt(item.Success, 10), strconv.FormatInt(item.Failure, 10),
			strconv.Itoa(item.Users), strconv.Itoa(item.Tokens), strconv.FormatInt(item.FirstSeenAt, 10), strconv.FormatInt(item.LastSeenAt, 10), info.Country, info.Region, info.City,
			strconv.FormatUint(uint64(info.ASN), 10), info.ASOrg, info.ISP, info.NetworkType, info.Proxy["vpn"], info.Proxy["public_proxy"], info.Proxy["residential_proxy"], info.Proxy["tor"], info.DataSource}
		for index, value := range record {
			record[index] = safeUserCSV(value)
		}
		_ = writer.Write(record)
	}
	writer.Flush()
	if writer.Error() != nil {
		c.JSON(503, gin.H{"error": "CSV 导出失败。"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="ip-analysis.csv"`)
	c.Data(200, "text/csv; charset=utf-8", body.Bytes())
}
func (n *networkStore) retentionPreview(ctx context.Context) (gin.H, error) {
	settings := n.maintenanceStatus().Settings
	cutoff := int64(0)
	counts := map[string]int64{}
	total := int64(0)
	if settings.RetentionDays > 0 {
		cutoff = n.clock().Unix() - int64(settings.RetentionDays)*86400
	}
	if cutoff > 0 {
		if err := n.ensure(ctx); err != nil {
			return nil, err
		}
		if n.service.config.Mode == "demo" {
			n.mu.Lock()
			for _, event := range n.events {
				if event.OccurredAt < cutoff {
					counts[event.Source]++
					total++
				}
			}
			n.mu.Unlock()
		} else {
			rows, err := n.service.management.query(ctx, "SELECT source,COUNT(*) FROM siteadmin_network_events WHERE occurred_at<? GROUP BY source", cutoff)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var source string
				var count int64
				if rows.Scan(&source, &count) != nil {
					return nil, errors.New("invalid retention count")
				}
				counts[source] = count
				total += count
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
	}
	return gin.H{"days": settings.RetentionDays, "cutoff": cutoff, "events": total, "source_counts": counts, "history_complete_after": false}, nil
}
func (s *dashboardService) networkRetentionPreviewHTTP(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	result, err := s.network.retentionPreview(ctx)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "过期事件预览失败。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}
func (s *dashboardService) networkRetentionCleanupHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request struct {
		Confirm string `json:"confirm"`
		Cutoff  int64  `json:"cutoff"`
		Reason  string `json:"reason"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Confirm != "清理过期事件" || strings.TrimSpace(request.Reason) == "" || !validLogFilter(request.Reason, 1000) {
		c.JSON(400, gin.H{"success": false, "error": "清理须填写原因和“清理过期事件”确认文字。"})
		return
	}
	n := s.network
	n.loadMaintenance()
	n.maintenance.mu.Lock()
	defer n.maintenance.mu.Unlock()
	settings := n.maintenance.state.Settings
	latest := n.clock().Unix() - int64(settings.RetentionDays)*86400
	if settings.RetentionDays == 0 || request.Cutoff <= 0 || request.Cutoff > latest || request.Cutoff < latest-600 {
		c.JSON(409, gin.H{"success": false, "error": "预览已失效或当前永久保留，请重新预览。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	for !n.syncMu.TryLock() {
		select {
		case <-ctx.Done():
			c.JSON(409, gin.H{"success": false, "error": "后台导入正在提交，请导入完成后重新预览清理。"})
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer n.syncMu.Unlock()
	if n.ensure(ctx) != nil {
		c.JSON(503, gin.H{"success": false, "error": "事件存储不可用。"})
		return
	}
	deleted := int64(0)
	id := fmt.Sprintf("retention-%d", time.Now().UnixNano())
	if s.config.Mode == "demo" {
		n.mu.Lock()
		for key, event := range n.events {
			if event.OccurredAt < request.Cutoff {
				delete(n.events, key)
				deleted++
			}
		}
		if deleted > 0 {
			n.meta["retention_cutoff"] = strconv.FormatInt(request.Cutoff, 10)
		}
		n.mu.Unlock()
		n.attributeMu.Lock()
		n.attributeAudit = append(n.attributeAudit, networkAttributeAudit{ID: id, At: n.clock().Unix(), IP: "retention", Reason: fmt.Sprintf("%s；已清理 %d 条事件；截止 %d", request.Reason, deleted, request.Cutoff), Operator: "站长后台管理员"})
		n.attributeMu.Unlock()
	} else {
		db := s.management
		tx, err := db.db.BeginTx(ctx, nil)
		if err != nil {
			c.JSON(503, gin.H{"success": false, "error": "清理事务不可用。"})
			return
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, bindQuery(db.driver, "DELETE FROM siteadmin_network_events WHERE occurred_at<?"), request.Cutoff)
		if err == nil {
			deleted, err = result.RowsAffected()
		}
		audit, _ := json.Marshal(gin.H{"id": id, "cutoff": request.Cutoff, "deleted": deleted, "reason": request.Reason, "at": n.clock().Unix()})
		if err == nil {
			_, err = tx.ExecContext(ctx, bindQuery(db.driver, "INSERT INTO siteadmin_network_audit (id,kind,subject,occurred_at,payload) VALUES (?,?,?,?,?)"), id, "retention", "network", n.clock().Unix(), string(audit))
		}
		if err == nil && deleted > 0 {
			query := "INSERT INTO siteadmin_network_meta (name,payload) VALUES (?,?)"
			if db.driver == "postgres" {
				query += " ON CONFLICT (name) DO UPDATE SET payload=EXCLUDED.payload"
			} else {
				query += " ON DUPLICATE KEY UPDATE payload=VALUES(payload)"
			}
			_, err = tx.ExecContext(ctx, bindQuery(db.driver, query), "retention_cutoff", strconv.FormatInt(request.Cutoff, 10))
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			c.JSON(503, gin.H{"success": false, "error": "清理未完成，请刷新事件与审计后重试。"})
			return
		}
	}
	n.revision.Add(1)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"deleted": deleted, "cutoff": request.Cutoff, "audit_id": id, "source_logs_unchanged": true}})
}

// A retained history can be complete for a recent window even after explicitly
// clearing older observations. Include the preceding baseline in start.
func (n *networkStore) historyCompleteFor(ctx context.Context, start int64) (bool, error) {
	if !n.service.config.NetworkHistoryComplete {
		return false, nil
	}
	raw, err := n.getMeta(ctx, "retention_cutoff")
	if err != nil {
		return false, err
	}
	if raw == "" {
		return true, nil
	}
	cutoff, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cutoff < 0 {
		return false, errors.New("invalid retention metadata")
	}
	return cutoff <= start, nil
}
