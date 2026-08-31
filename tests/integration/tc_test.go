package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos/tc"
)

type recordingTcRunner struct {
	root      string
	failMatch string
	commands  []string
}

func (runner *recordingTcRunner) Run(_ context.Context, arguments ...string) ([]byte, error) {
	command := strings.Join(arguments, " ")
	runner.commands = append(runner.commands, command)
	if runner.failMatch != "" && strings.Contains(command, runner.failMatch) {
		runner.failMatch = ""
		return nil, fmt.Errorf("injected tc failure")
	}
	if command == "qdisc show dev eth0" {
		switch runner.root {
		case "argo":
			return []byte("qdisc htb a400: root refcnt 2\n"), nil
		case "unrelated":
			return []byte("qdisc fq_codel 0: root refcnt 2\n"), nil
		default:
			return nil, nil
		}
	}
	if strings.HasPrefix(command, "qdisc replace dev eth0 root handle a400:") {
		runner.root = "argo"
	}
	if command == "qdisc del dev eth0 root handle a400:" {
		runner.root = ""
	}

	return nil, nil
}

func TestTcBackendApplyCleanupAndConflict(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingTcRunner{}
	backend, err := tc.NewWithRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if runner.root != "argo" {
		t.Fatal("Argo tc root was not applied")
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if runner.root != "" {
		t.Fatal("Argo tc root remains after cleanup")
	}
	runner.root = "unrelated"
	err = backend.Apply(context.Background(), tree)
	var conflict tc.RootConflictError
	if !errors.As(err, &conflict) || runner.root != "unrelated" {
		t.Fatalf("unrelated root was not preserved: %v, %s", err, runner.root)
	}
}

func TestTcBackendCleansPartialTreeAfterFailure(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingTcRunner{failMatch: "classid a400:10"}
	backend, err := tc.NewWithRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), tree); err == nil {
		t.Fatal("injected tc failure succeeded")
	}
	if runner.root != "" {
		t.Fatal("partial Argo tc root remains after failure")
	}
	if !strings.Contains(strings.Join(runner.commands, "\n"), "qdisc del dev eth0 root handle a400:") {
		t.Fatal("failure did not trigger owned-root cleanup")
	}
}
