package domain

// Threshold holds the alert limits for one project.
// A zero value disables the corresponding rule.
type Threshold struct {
	P95Ms       float64 `json:"p95_ms"`
	ErrorRate   float64 `json:"error_rate"`
	RPSMax      float64 `json:"rps_max"`
	ReqPerIPMax float64 `json:"req_per_ip_max"`
}

// DefaultThreshold mirrors the schema defaults.
func DefaultThreshold() Threshold {
	return Threshold{
		P95Ms:       1000,
		ErrorRate:   5,
		RPSMax:      100,
		ReqPerIPMax: 30,
	}
}

// Validate rejects impossible values. Zero is allowed and disables a rule.
func (t Threshold) Validate() error {
	switch {
	case t.P95Ms < 0:
		return Validationf("p95_ms must be non-negative")
	case t.ErrorRate < 0 || t.ErrorRate > 100:
		return Validationf("error_rate must be between 0 and 100")
	case t.RPSMax < 0:
		return Validationf("rps_max must be non-negative")
	case t.ReqPerIPMax < 0:
		return Validationf("req_per_ip_max must be non-negative")
	}
	return nil
}
