package server

import (
	"net/http"
	"time"

)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, api-key")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET")
		return
	}
	cfg := s.cfg.Snapshot()
	now := time.Now().Unix()
	specs := s.gw.Catalog().List()
	data := make([]map[string]any, 0, len(specs))
	for _, m := range specs {
		item := map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": "orcaterm",
			"name":     m.DisplayName,
			"upstream": m.Upstream,
		}
		if m.Description != "" {
			item["description"] = m.Description
		}
		if len(m.Capabilities) > 0 {
			item["capabilities"] = m.Capabilities
		}
		if len(m.Tags) > 0 {
			item["tags"] = m.Tags
		}
		item["free"] = m.Free
		item["price_rate"] = m.PriceRate
		item["show_price_rate"] = m.ShowPriceRate
		item["support_image"] = m.SupportImg
		if m.ContextLength > 0 {
			item["context_length"] = m.ContextLength
		}
		if m.ShowThinking && len(m.ThinkingLevels) > 0 {
			levels := make([]map[string]any, 0, len(m.ThinkingLevels))
			for _, lv := range m.ThinkingLevels {
				levels = append(levels, map[string]any{
					"label":   lv.Label,
					"value":   lv.Value,
					"default": lv.Default,
				})
			}
			item["thinking"] = map[string]any{
				"supported": true,
				"levels":    levels,
			}
		} else {
			item["thinking"] = map[string]any{"supported": false}
		}
		data = append(data, item)
	}
	// 别名也列出来，便于客户端直接选。
	for alias, target := range cfg.Models.Aliases {
		data = append(data, map[string]any{
			"id":       alias,
			"object":   "model",
			"created":  now,
			"owned_by": "orcaterm",
			"alias_of": target,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

