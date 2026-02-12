package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration wraps time.Duration to provide JSON marshal/unmarshal as a Go
// duration string (e.g. "5m", "24h", "30s").
type Duration time.Duration

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("Duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}
