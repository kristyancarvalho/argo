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
	ifb       bool
	alias     string
	ingress   bool
	marker    bool
	redirect  bool
	unrelated bool
	conflict  string
}

func (runner *recordingTcRunner) Run(_ context.Context, arguments ...string) ([]byte, error) {
	command := strings.Join(arguments, " ")
	runner.commands = append(runner.commands, command)
	if runner.failMatch != "" && strings.Contains(command, runner.failMatch) {
		runner.failMatch = ""
		return nil, fmt.Errorf("injected tc failure")
	}
	if strings.HasPrefix(command, "-details -o link show dev argo") {
		if !runner.ifb {
			return nil, fmt.Errorf("device missing")
		}
		return []byte("link details ifb alias " + runner.alias), nil
	}
	if strings.HasPrefix(command, "link add name argo") {
		runner.ifb = true
		return nil, nil
	}
	if strings.Contains(command, " alias argo-rx:eth0") {
		runner.alias = "argo-rx:eth0"
		return nil, nil
	}
	if strings.HasPrefix(command, "link set dev argo") {
		return nil, nil
	}
	if strings.HasPrefix(command, "link delete dev argo") {
		runner.ifb = false
		runner.alias = ""
		runner.root = ""
		return nil, nil
	}
	if command == "qdisc show dev eth0" {
		result := "qdisc noqueue 0: root refcnt 2\n"
		if runner.ingress {
			result += "qdisc ingress ffff: parent ffff:fff1\n"
		}
		return []byte(result), nil
	}
	if strings.HasPrefix(command, "qdisc show dev argo") {
		switch runner.root {
		case "argo":
			return []byte("qdisc htb a400: root refcnt 2\n"), nil
		case "unrelated":
			return []byte("qdisc fq_codel 0: root refcnt 2\n"), nil
		case "noqueue":
			return []byte("qdisc noqueue 0: root refcnt 2\n"), nil
		default:
			return nil, nil
		}
	}
	if command == "qdisc add dev eth0 handle ffff: ingress" {
		runner.ingress = true
	}
	if command == "qdisc del dev eth0 ingress" {
		runner.ingress = false
	}
	if strings.Contains(command, "pref 16399") && strings.HasPrefix(command, "filter add") {
		runner.marker = true
	}
	if strings.Contains(command, "pref 16400") && strings.HasPrefix(command, "filter add") {
		runner.redirect = true
	}
	if command == "filter del dev eth0 ingress protocol all pref 16399 handle 1 matchall" {
		runner.marker = false
	}
	if command == "filter del dev eth0 ingress protocol all pref 16400 handle 1 matchall" {
		runner.redirect = false
	}
	if command == "filter show dev eth0 ingress pref 16399" {
		if runner.conflict == "16399" {
			return []byte("filter flower action pass"), nil
		}
		if runner.marker {
			return []byte("filter matchall gact action continue"), nil
		}
		return nil, nil
	}
	if command == "filter show dev eth0 ingress pref 16400" {
		if runner.conflict == "16400" {
			return []byte("filter flower action pass"), nil
		}
		if runner.redirect {
			return []byte("filter matchall ctinfo Redirect to device " + tc.IFBInterface("eth0")), nil
		}
		return nil, nil
	}
	if command == "filter show dev eth0 ingress" {
		var filters string
		if runner.marker {
			filters += "filter matchall gact action continue\n"
		}
		if runner.redirect {
			filters += "filter matchall ctinfo Redirect to device " + tc.IFBInterface("eth0") + "\n"
		}
		if runner.unrelated {
			filters += "filter flower action pass\n"
		}
		return []byte(filters), nil
	}
	if strings.HasPrefix(command, "qdisc replace dev argo") && strings.Contains(command, " root handle a400:") {
		runner.root = "argo"
	}
	if strings.HasPrefix(command, "qdisc del dev argo") && strings.HasSuffix(command, "root handle a400:") {
		runner.root = ""
	}

	return nil, nil
}

func TestTcBackendPreservesExistingIngressState(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingTcRunner{ingress: true, unrelated: true}
	backend, err := tc.NewWithRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if !runner.redirect || runner.marker {
		t.Fatalf("unexpected ingress ownership state: %+v", runner)
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if !runner.ingress || !runner.unrelated || runner.redirect || runner.ifb {
		t.Fatalf("existing ingress state was not preserved: %+v", runner)
	}
}

func TestTcBackendRejectsOccupiedArgoIngressSlot(t *testing.T) {
	tree, err := tc.GenerateTree("eth0", 100_000_000, 60_000_000)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingTcRunner{ingress: true, conflict: "16400"}
	backend, err := tc.NewWithRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	err = backend.Apply(context.Background(), tree)
	var conflict tc.IngressConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("occupied ingress slot returned %v", err)
	}
	if !runner.ingress || runner.ifb {
		t.Fatalf("conflicting ingress state was modified: %+v", runner)
	}
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
		t.Fatal("Argo IFB tc root was not applied")
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if runner.root != "" {
		t.Fatal("Argo IFB tc root remains after cleanup")
	}
	runner.root = "noqueue"
	if err := backend.Apply(context.Background(), tree); err != nil {
		t.Fatalf("kernel noqueue root was rejected: %v", err)
	}
	if runner.root != "argo" {
		t.Fatal("Argo tc root did not replace noqueue")
	}
	if err := backend.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	runner.root = "unrelated"
	runner.ifb = true
	runner.alias = "someone-else"
	err = backend.Apply(context.Background(), tree)
	var conflict tc.IFBConflictError
	if !errors.As(err, &conflict) || runner.root != "unrelated" {
		t.Fatalf("unrelated IFB was not preserved: %v, %s", err, runner.root)
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
	if !strings.Contains(strings.Join(runner.commands, "\n"), "link delete dev "+tc.IFBInterface("eth0")) {
		t.Fatal("failure did not trigger owned IFB cleanup")
	}
}
