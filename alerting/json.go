package alerting

// ActiveJSON renders firing alerts as generic maps.
//
// This satisfies server.AlertSource without the server package importing this
// one, keeping the dependency direction one-way (main wires them together).
func (e *Evaluator) ActiveJSON() []map[string]interface{} {
	return alertsToJSON(e.Active())
}

// HistoryJSON renders recent alert transitions as generic maps.
func (e *Evaluator) HistoryJSON() []map[string]interface{} {
	return alertsToJSON(e.History())
}

func alertsToJSON(alerts []Alert) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, map[string]interface{}{
			"rule":      a.Rule,
			"server_id": a.ServerID,
			"tenant_id": a.TenantID,
			"metric":    a.Metric,
			"severity":  string(a.Severity),
			"state":     string(a.State),
			"value":     a.Value,
			"threshold": a.Threshold,
			"message":   a.Message,
			"title":     a.Title(),
			"fired_at":  a.FiredAt.Unix(),
			"resolved":  a.Resolved,
		})
	}
	return out
}
