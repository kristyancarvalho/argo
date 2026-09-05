package unit_test

import (
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestTrafficPolicyMapping(t *testing.T) {
	tests := []struct {
		policy   qos.Policy
		expected uint64
	}{
		{qos.PolicyBalanced, 50_000_000},
		{qos.PolicyThroughput, 80_000_000},
		{qos.PolicyFocus, 20_000_000},
	}
	for _, test := range tests {
		t.Run(string(test.policy), func(t *testing.T) {
			state, err := qos.MapPolicy(test.policy, qos.PolicyEnvironment{
				Interface:             "eth0",
				LinkRateBitsPerSecond: 100_000_000,
				Cgroup:                qos.CgroupSelector{Path: "argo.service", Level: 1},
				ActiveDownloads:       2,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !state.Enabled || state.Policy != test.policy ||
				state.ArgoRateBitsPerSecond != test.expected || state.Interface != "eth0" {
				t.Fatalf("unexpected desired state: %+v", state)
			}
		})
	}
}

func TestTrafficPolicyMappingTurnsOffWithoutActiveDownloads(t *testing.T) {
	for _, policy := range []qos.Policy{qos.PolicyOff, qos.PolicyBalanced, qos.PolicyThroughput, qos.PolicyFocus} {
		state, err := qos.MapPolicy(policy, qos.PolicyEnvironment{})
		if err != nil {
			t.Fatal(err)
		}
		if state != (qos.DesiredState{Policy: qos.PolicyOff}) {
			t.Fatalf("policy %s mapped to %+v", policy, state)
		}
	}
}

func TestTrafficPolicyMappingRejectsUnavailableInputs(t *testing.T) {
	tests := []qos.PolicyEnvironment{
		{Interface: "", LinkRateBitsPerSecond: 100, Cgroup: qos.CgroupSelector{Path: "argo.service", Level: 1}, ActiveDownloads: 1},
		{Interface: "eth0", LinkRateBitsPerSecond: 0, Cgroup: qos.CgroupSelector{Path: "argo.service", Level: 1}, ActiveDownloads: 1},
		{Interface: "eth0", LinkRateBitsPerSecond: 100, Cgroup: qos.CgroupSelector{}, ActiveDownloads: 1},
	}
	for _, environment := range tests {
		if _, err := qos.MapPolicy(qos.PolicyBalanced, environment); err == nil {
			t.Fatalf("environment %+v was accepted", environment)
		}
	}
	if _, err := qos.MapPolicy(qos.PolicyLatency, qos.PolicyEnvironment{ActiveDownloads: 1}); err == nil {
		t.Fatal("latency policy was accepted")
	}
}
