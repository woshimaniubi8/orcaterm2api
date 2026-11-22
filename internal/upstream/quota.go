package upstream

import (
	"context"
	"encoding/json"
)

// Quota 是账号维度的用量额度（OrcaTerm 的 hunyuan_ai 配额）。
type Quota struct {
	Feature    string `json:"feature"`
	Limit      int    `json:"quota_limit"`
	Used       int    `json:"quota_used"`
	StartDate  string `json:"quota_start_date"`
	EndDate    string `json:"quota_end_date"`
	Remaining  int    `json:"remaining"`
	PercentUse float64 `json:"percent_used"`
}

// AIQuota 查询账号的 AI 用量额度。
func (c *Client) AIQuota(ctx context.Context, accessToken string) (*Quota, error) {
	raw, err := c.cgiCall(ctx, "DescribeUserUsageStats", map[string]any{"feature": "hunyuan_ai"}, accessToken)
	if err != nil {
		return nil, err
	}
	var r struct {
		Feature       string `json:"feature"`
		QuotaLimit    int    `json:"quotaLimit"`
		QuotaUsed     int    `json:"quotaUsed"`
		QuotaStart    string `json:"quotaStartDate"`
		QuotaEnd      string `json:"quotaEndDate"`
		ApplicationID any    `json:"applicationId"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "用量解析失败: " + err.Error()}
	}
	q := &Quota{
		Feature:   r.Feature,
		Limit:     r.QuotaLimit,
		Used:      r.QuotaUsed,
		StartDate: r.QuotaStart,
		EndDate:   r.QuotaEnd,
	}
	if q.Feature == "" {
		q.Feature = "hunyuan_ai"
	}
	q.Remaining = q.Limit - q.Used
	if q.Limit > 0 {
		q.PercentUse = float64(q.Used) / float64(q.Limit) * 100
	}
	return q, nil
}

