package unit_test

import (
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestQoSPolicyValidation(t *testing.T) {
	for _, name := range []string{"off", "balanced", "throughput", "latency", "focus"} {
		policy, err := qos.ParsePolicy(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(policy) != name {
			t.Fatalf("parsed policy is %q, expected %q", policy, name)
		}
	}
	for _, name := range []string{"", "maximum", "BALANCED"} {
		if _, err := qos.ParsePolicy(name); err == nil {
			t.Fatalf("invalid policy %q succeeded", name)
		}
	}
}

func TestQoSDesiredStateCalculation(t *testing.T) {
	off, err := qos.CalculateDesiredState(qos.Intent{Policy: qos.PolicyOff, Interface: "wlan0"})
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled || off.Policy != qos.PolicyOff || off.Interface != "" {
		t.Fatalf("unexpected off state: %+v", off)
	}
	enabled, err := qos.CalculateDesiredState(qos.Intent{
		Policy:    qos.PolicyBalanced,
		Interface: "wlan0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Policy != qos.PolicyBalanced || enabled.Interface != "wlan0" {
		t.Fatalf("unexpected enabled state: %+v", enabled)
	}
}

func TestQoSDesiredStateRejectsInvalidInput(t *testing.T) {
	tests := []qos.Intent{
		{Policy: "maximum", Interface: "eth0"},
		{Policy: qos.PolicyBalanced},
		{Policy: qos.PolicyBalanced, Interface: "interface-name-too-long"},
		{Policy: qos.PolicyBalanced, Interface: "eth 0"},
		{Policy: qos.PolicyBalanced, Interface: "../eth0"},
	}
	for _, intent := range tests {
		if _, err := qos.CalculateDesiredState(intent); err == nil {
			t.Fatalf("invalid intent succeeded: %+v", intent)
		}
	}
	invalidStates := []qos.DesiredState{
		{Policy: qos.PolicyBalanced},
		{Enabled: true, Policy: qos.PolicyOff, Interface: "eth0"},
		{Enabled: true, Policy: qos.PolicyFocus, Interface: ""},
	}
	for _, state := range invalidStates {
		if err := state.Validate(); err == nil {
			t.Fatalf("invalid state succeeded: %+v", state)
		}
	}
}
