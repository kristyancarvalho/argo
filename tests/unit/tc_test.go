package unit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/tc"
)

func TestTcDesiredTreeGeneration(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if tree.ShapingRateBitsPerSecond != 95_000_000 || tree.DefaultRateBitsPerSecond != 38_000_000 || len(tree.Commands) != 7 {
		t.Fatalf("unexpected tc tree: %+v", tree)
	}
	joined := make([]string, len(tree.Commands))
	for index, command := range tree.Commands {
		joined[index] = strings.Join(command.Arguments, " ")
	}
	commands := strings.Join(joined, "\n")
	if tree.IFBInterface != tc.IFBInterface("eth0") || tree.IFBInterface == tree.Interface {
		t.Fatalf("unexpected IFB interface: %+v", tree)
	}
	for _, expected := range []string{
		"dev " + tree.IFBInterface + " root handle a400: htb default 20",
		"classid a400:10 htb rate 57000000bit ceil 95000000bit",
		"classid a400:20 htb rate 38000000bit ceil 95000000bit",
		"parent a400:10 handle a410: fq_codel",
		"parent a400:20 handle a420: fq_codel",
		"handle 0xa400 fw classid a400:10",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("tc commands %q do not contain %q", commands, expected)
		}
	}
}

func TestTcAdaptiveTreeEnforcesArgoCeiling(t *testing.T) {
	state := qos.DesiredState{
		Enabled:               true,
		Policy:                qos.PolicyLatency,
		Interface:             "eth0",
		LinkRateBitsPerSecond: 100_000_000,
		ArgoRateBitsPerSecond: 20_000_000,
		Cgroup:                qos.CgroupSelector{Path: "user.slice/argo.scope", Level: 2},
	}
	tree, err := tc.GenerateTreeForState(state)
	if err != nil {
		t.Fatal(err)
	}
	if tree.ArgoCeilingBitsPerSecond != 20_000_000 {
		t.Fatalf("adaptive Argo ceiling is %d", tree.ArgoCeilingBitsPerSecond)
	}
	commands := make([]string, 0, len(tree.Commands))
	for _, command := range tree.Commands {
		commands = append(commands, strings.Join(command.Arguments, " "))
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "classid a400:10 htb rate 19000000bit ceil 19000000bit") {
		t.Fatalf("adaptive tree does not enforce its reported limit: %s", joined)
	}

	state.Policy = qos.PolicyThroughput
	staticTree, err := tc.GenerateTreeForState(state)
	if err != nil {
		t.Fatal(err)
	}
	if staticTree.ArgoCeilingBitsPerSecond != 100_000_000 {
		t.Fatalf("static policy lost borrowing ceiling: %+v", staticTree)
	}
}

func TestTcRejectsInvalidBandwidthAndMissingExecutable(t *testing.T) {
	for _, rates := range [][2]uint64{{0, 1}, {100, 0}, {100, 100}, {100, 101}} {
		if _, err := tc.GenerateTree("eth0", rates[0], rates[1]); err == nil {
			t.Fatalf("invalid rates succeeded: %v", rates)
		}
	}
	for _, ceiling := range []uint64{49, 101} {
		if _, err := tc.GenerateTreeWithArgoCeiling("eth0", 100, 50, ceiling); err == nil {
			t.Fatalf("invalid Argo ceiling %d succeeded", ceiling)
		}
	}
	if _, err := tc.GenerateTree("eth 0", 100, 50); err == nil {
		t.Fatal("invalid interface succeeded")
	}
	if len(tc.IFBInterface("interface-with-a-long-name")) > 15 {
		t.Fatal("derived IFB name exceeds Linux interface limit")
	}
	_, err := tc.NewWithExecutable("argo-tc-command-that-does-not-exist")
	var unavailable tc.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing tc returned %v", err)
	}
}
