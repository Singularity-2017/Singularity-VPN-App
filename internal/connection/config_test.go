package connection

import (
	"testing"
	"time"
)

func Test_default_config_matches_the_documented_values(t *testing.T) {
	config := Default_config()

	if config.Connect_timeout != 30*time.Second {
		t.Errorf("Connect_timeout = %v, want 30s", config.Connect_timeout)
	}
	if config.Protocol_timeout != 5*time.Second {
		t.Errorf("Protocol_timeout = %v, want 5s", config.Protocol_timeout)
	}
	if config.Monitor_interval != 5*time.Second {
		t.Errorf("Monitor_interval = %v, want 5s", config.Monitor_interval)
	}
	if config.Reevaluation_interval != 5*time.Minute {
		t.Errorf("Reevaluation_interval = %v, want 5m", config.Reevaluation_interval)
	}
	if config.Profile_duration_days != 30 {
		t.Errorf("Profile_duration_days = %d, want 30", config.Profile_duration_days)
	}
	if config.Recovery.Max_switch_attempts == 0 {
		t.Errorf("Recovery = %+v, want recovery.Default_config()", config.Recovery)
	}
}
