package unit_test

import (
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestClassificationRuleGeneration(t *testing.T) {
	plan, err := qos.GenerateClassification("wlan0", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Enabled || plan.Interface != "wlan0" ||
		plan.ArgoRule.Class != qos.TrafficClassArgo ||
		plan.ArgoRule.CgroupID != 42 ||
		plan.ArgoRule.PacketMark != qos.ArgoPacketMark ||
		plan.ArgoRule.MarkMask != qos.ArgoPacketMarkMask ||
		plan.DefaultClass != qos.TrafficClassDefault {
		t.Fatalf("unexpected classification plan: %+v", plan)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClassificationRejectsInvalidPlans(t *testing.T) {
	if _, err := qos.GenerateClassification("", 42); err == nil {
		t.Fatal("classification without interface succeeded")
	}
	if _, err := qos.GenerateClassification("eth0", 0); err == nil {
		t.Fatal("classification without cgroup succeeded")
	}
	valid, err := qos.GenerateClassification("eth0", 42)
	if err != nil {
		t.Fatal(err)
	}
	tests := []qos.ClassificationPlan{
		{Interface: "eth0"},
		func() qos.ClassificationPlan {
			plan := valid
			plan.ArgoRule.Class = qos.TrafficClassDefault
			return plan
		}(),
		func() qos.ClassificationPlan {
			plan := valid
			plan.ArgoRule.PacketMark = 1
			return plan
		}(),
		func() qos.ClassificationPlan {
			plan := valid
			plan.DefaultClass = qos.TrafficClassArgo
			return plan
		}(),
	}
	for _, plan := range tests {
		if err := plan.Validate(); err == nil {
			t.Fatalf("invalid classification plan succeeded: %+v", plan)
		}
	}
}
