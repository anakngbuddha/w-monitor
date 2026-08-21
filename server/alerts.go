package server

import (
	"encoding/json"
	"net/http"
)

// AlertSource exposes current alert state to the HTTP layer.
//
// Declared as an interface here so the server package does not import the
// alerting package: the dependency runs one way, from main, which wires the two
// together.
type AlertSource interface {
	// ActiveJSON returns currently firing alerts.
	ActiveJSON() []map[string]interface{}
	// HistoryJSON returns recent alert transitions, newest first.
	HistoryJSON() []map[string]interface{}
}

// SetAlertSource registers the alert endpoint. Without a source, /api/alerts is
// not served at all rather than returning a misleading empty list that looks
// like "nothing is wrong".
func (s *Server) SetAlertSource(src AlertSource) {
	if src == nil {
		return
	}
	s.alerts = src
	s.mux.HandleFunc("/api/alerts", s.handleAlerts)
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}
	if s.alerts == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "alerting is not enabled")
		return
	}

	active := filterByTenant(s.alerts.ActiveJSON(), tenantID)
	history := filterByTenant(s.alerts.HistoryJSON(), tenantID)

	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"active":       active,
		"active_count": len(active),
		"history":      history,
	})
}

// filterByTenant drops alerts belonging to other tenants.
//
// Alerts carry the same tenant scoping as metrics: without this, a hub client
// could read the alert text (including hostnames and utilisation figures) of
// every other client on the hub.
func filterByTenant(alerts []map[string]interface{}, tenantID string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(alerts))
	if tenantID == "" {
		return append(out, alerts...)
	}
	for _, a := range alerts {
		if v, ok := a["tenant_id"].(string); ok && v == tenantID {
			out = append(out, a)
		}
	}
	return out
}
