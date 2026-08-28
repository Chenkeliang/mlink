package manifest

import "testing"

func TestValidateRuntimeAcceptsNarrowerCapabilities(t *testing.T) {
	declared := map[string]CapabilityDescriptor{
		"capture_turn": {
			Version: 2, Roles: []string{"user", "assistant"}, MaxRequestBytes: 1024,
			MaxInFlight: 8, ReplaySafe: true, Ordering: "turn",
		},
		"recall": {
			Version: 2, Scopes: []string{"user", "agent"}, MaxRequestBytes: 2048,
			MaxResultItems: 20, MaxInFlight: 8,
		},
		"health": {Version: 1, MaxInFlight: 4},
	}
	runtime := map[string]CapabilityDescriptor{
		"capture_turn": {
			Version: 1, Roles: []string{"user"}, MaxRequestBytes: 512,
			MaxInFlight: 2, ReplaySafe: false, Ordering: "turn",
		},
		"recall": {
			Version: 1, Scopes: []string{"user"}, MaxRequestBytes: 1024,
			MaxResultItems: 5, MaxInFlight: 4,
		},
		"health": {Version: 1},
	}

	normalized, err := ValidateRuntime(declared, runtime)
	if err != nil {
		t.Fatalf("ValidateRuntime() error = %v", err)
	}
	if normalized["health"].MaxInFlight != 4 {
		t.Fatalf("health MaxInFlight = %d, want default 4", normalized["health"].MaxInFlight)
	}
	runtimeHealth := runtime["health"]
	runtimeHealth.MaxInFlight = 64
	runtime["health"] = runtimeHealth
	if normalized["health"].MaxInFlight != 4 {
		t.Fatal("returned capability map aliases caller input")
	}
}

func TestValidateRuntimeRejectsCapabilityEscalation(t *testing.T) {
	baseDeclared := map[string]CapabilityDescriptor{
		"capture_turn": {
			Version: 1, Roles: []string{"user"}, MaxRequestBytes: 1024,
			MaxInFlight: 4, ReplaySafe: false, Ordering: "turn",
		},
	}
	tests := []struct {
		name    string
		runtime CapabilityDescriptor
	}{
		{name: "higher version", runtime: CapabilityDescriptor{Version: 2, Roles: []string{"user"}, MaxRequestBytes: 1024, MaxInFlight: 4, Ordering: "turn"}},
		{name: "extra role", runtime: CapabilityDescriptor{Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 1024, MaxInFlight: 4, Ordering: "turn"}},
		{name: "larger request", runtime: CapabilityDescriptor{Version: 1, Roles: []string{"user"}, MaxRequestBytes: 2048, MaxInFlight: 4, Ordering: "turn"}},
		{name: "larger concurrency", runtime: CapabilityDescriptor{Version: 1, Roles: []string{"user"}, MaxRequestBytes: 1024, MaxInFlight: 5, Ordering: "turn"}},
		{name: "replay safety escalation", runtime: CapabilityDescriptor{Version: 1, Roles: []string{"user"}, MaxRequestBytes: 1024, MaxInFlight: 4, ReplaySafe: true, Ordering: "turn"}},
		{name: "different ordering", runtime: CapabilityDescriptor{Version: 1, Roles: []string{"user"}, MaxRequestBytes: 1024, MaxInFlight: 4, Ordering: "message"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ValidateRuntime(baseDeclared, map[string]CapabilityDescriptor{"capture_turn": tt.runtime}); err == nil {
				t.Fatal("ValidateRuntime() error = nil, want escalation error")
			}
		})
	}

	if _, err := ValidateRuntime(baseDeclared, map[string]CapabilityDescriptor{
		"health": {Version: 1, MaxInFlight: 1},
	}); err == nil {
		t.Fatal("ValidateRuntime() accepted undeclared capability")
	}
}
