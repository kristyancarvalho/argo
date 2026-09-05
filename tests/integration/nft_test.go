package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/nft"
)

type nftInvocation struct {
	input     string
	arguments []string
}

type recordingNftRunner struct {
	table       bool
	invocations []nftInvocation
}

func (runner *recordingNftRunner) Run(
	_ context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	runner.invocations = append(runner.invocations, nftInvocation{
		input:     input,
		arguments: append([]string(nil), arguments...),
	})
	command := strings.Join(arguments, " ")
	switch command {
	case "list tables":
		if runner.table {
			return []byte("table inet unrelated\ntable inet argo\n"), nil
		}
		return []byte("table inet unrelated\n"), nil
	case "delete table inet argo":
		if !runner.table {
			return nil, fmt.Errorf("table missing")
		}
		runner.table = false
		return nil, nil
	case "-f -":
		if !strings.Contains(input, "table inet argo") {
			return nil, fmt.Errorf("missing owned table")
		}
		if runner.table && !strings.HasPrefix(input, "delete table inet argo\n") {
			return nil, fmt.Errorf("existing table was not replaced atomically")
		}
		runner.table = true
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected nft invocation %q", command)
	}
}

func TestNftablesBackendAppliesIdempotentlyAndCleansUp(t *testing.T) {
	runner := &recordingNftRunner{}
	backend, err := nft.NewWithRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := qos.GenerateClassification("eth0", qos.CgroupSelector{
		Path: "user.slice/argo.service", Level: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.ApplyClassification(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := backend.ApplyClassification(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !runner.table {
		t.Fatal("owned nftables table was not applied")
	}
	applies := 0
	for _, invocation := range runner.invocations {
		switch strings.Join(invocation.arguments, " ") {
		case "-f -":
			applies++
		}
	}
	if applies != 2 {
		t.Fatalf("unexpected apply lifecycle: %+v", runner.invocations)
	}
	if err := backend.RemoveClassification(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if err := backend.RemoveClassification(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if runner.table {
		t.Fatal("owned nftables table remains after cleanup")
	}
	if err := backend.RemoveClassification(context.Background(), "eth 0"); err == nil {
		t.Fatal("invalid cleanup interface succeeded")
	}
}
