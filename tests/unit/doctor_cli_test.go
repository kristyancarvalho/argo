package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/doctor"
)

func TestCLIDoctorRendersHumanAndVersionedJSON(t *testing.T) {
	report := doctor.Report{
		CoreReady: true,
		QoSReady:  false,
		Checks: []doctor.Check{
			{Name: "daemon", Scope: "core", State: doctor.StateAvailable, Detail: "running"},
			{Name: "qos_helper", Scope: "optional", State: doctor.StateUnavailable, Detail: "missing", Action: "start helper"},
		},
	}
	diagnostics := func(context.Context) doctor.Report { return report }
	var human bytes.Buffer
	if err := cli.RunWithOptions(context.Background(), nil, &human, []string{"doctor"}, cli.Options{Doctor: diagnostics}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"daemon", "available", "qos_helper", "action: start helper", "Core downloads", "ready", "System QoS", "unavailable"} {
		if !strings.Contains(human.String(), expected) {
			t.Fatalf("human doctor output %q lacks %q", human.String(), expected)
		}
	}
	var machine bytes.Buffer
	if err := cli.RunWithOptions(context.Background(), nil, &machine, []string{"doctor", "--json"}, cli.Options{Color: true, Doctor: diagnostics}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(machine.String(), "\x1b[") {
		t.Fatalf("doctor JSON contains terminal styling: %q", machine.String())
	}
	var document struct {
		SchemaVersion int           `json:"schema_version"`
		Kind          string        `json:"kind"`
		Data          doctor.Report `json:"data"`
	}
	if err := json.Unmarshal(machine.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != cli.JSONSchemaVersion || document.Kind != "doctor" || !document.Data.CoreReady || document.Data.QoSReady {
		t.Fatalf("unexpected doctor JSON: %+v", document)
	}
}

func TestCLIDoctorReturnsDaemonUnavailableForFailedCore(t *testing.T) {
	report := doctor.Report{Checks: []doctor.Check{{Name: "daemon", Scope: "core", State: doctor.StateUnavailable}}}
	err := cli.RunWithOptions(
		context.Background(),
		nil,
		&bytes.Buffer{},
		[]string{"doctor", "--json"},
		cli.Options{Doctor: func(context.Context) doctor.Report { return report }},
	)
	var doctorError cli.DoctorError
	if !errors.As(err, &doctorError) || cli.ExitCode(err) != cli.ExitDaemonUnavailable {
		t.Fatalf("failed doctor returned %v with exit %d", err, cli.ExitCode(err))
	}
}
