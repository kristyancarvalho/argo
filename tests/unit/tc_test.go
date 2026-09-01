package unit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos/tc"
)

func TestTcDesiredTreeGeneration(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if tree.DefaultRateBitsPerSecond != 40_000_000 || len(tree.Commands) != 7 {
		t.Fatalf("unexpected tc tree: %+v", tree)
	}
	joined := make([]string, len(tree.Commands))
	for index, command := range tree.Commands {
		joined[index] = strings.Join(command.Arguments, " ")
	}
	commands := strings.Join(joined, "\n")
	for _, expected := range []string{
		"root handle a400: htb default 20",
		"classid a400:10 htb rate 60000000bit ceil 100000000bit",
		"classid a400:20 htb rate 40000000bit ceil 100000000bit",
		"parent a400:10 handle a410: fq_codel",
		"parent a400:20 handle a420: fq_codel",
		"handle 0xa400 fw classid a400:10",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("tc commands %q do not contain %q", commands, expected)
		}
	}
}

func TestTcRejectsInvalidBandwidthAndMissingExecutable(t *testing.T) {
	for _, rates := range [][2]uint64{{0, 1}, {100, 0}, {100, 100}, {100, 101}} {
		if _, err := tc.GenerateTree("eth0", rates[0], rates[1]); err == nil {
			t.Fatalf("invalid rates succeeded: %v", rates)
		}
	}
	if _, err := tc.GenerateTree("eth 0", 100, 50); err == nil {
		t.Fatal("invalid interface succeeded")
	}
	_, err := tc.NewWithExecutable("argo-tc-command-that-does-not-exist")
	var unavailable tc.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing tc returned %v", err)
	}
}
