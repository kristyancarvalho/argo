package nft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/kristyancarvalho/argo/internal/qos"
)

const (
	tableFamily = "inet"
	tableName   = "argo"
	chainName   = "output"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type Backend struct {
	runner Runner
	mutex  sync.Mutex
}

type commandRunner struct {
	path string
}

type UnavailableError struct {
	Reason string
}

func (err UnavailableError) Error() string {
	return "nftables is unavailable: " + err.Reason
}

func New() (*Backend, error) {
	return NewWithExecutable("nft")
}

func NewWithExecutable(name string) (*Backend, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, UnavailableError{Reason: err.Error()}
	}

	return NewWithRunner(commandRunner{path: path})
}

func NewWithRunner(runner Runner) (*Backend, error) {
	if runner == nil {
		return nil, fmt.Errorf("nftables runner is required")
	}

	return &Backend{runner: runner}, nil
}

func (backend *Backend) ApplyClassification(ctx context.Context, plan qos.ClassificationPlan) error {
	ruleset, err := Ruleset(plan)
	if err != nil {
		return err
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	exists, err := backend.tableExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		ruleset = "delete table " + tableFamily + " " + tableName + "\n" + ruleset
	}
	if _, err := backend.runner.Run(ctx, ruleset, "-f", "-"); err != nil {
		return fmt.Errorf("apply Argo nftables classification: %w", err)
	}

	return nil
}

func (backend *Backend) RemoveClassification(ctx context.Context, interfaceName string) error {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return err
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	exists, err := backend.tableExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := backend.runner.Run(ctx, "", "delete", "table", tableFamily, tableName); err != nil {
		return fmt.Errorf("remove Argo nftables table: %w", err)
	}

	return nil
}

func (backend *Backend) tableExists(ctx context.Context) (bool, error) {
	output, err := backend.runner.Run(ctx, "", "list", "tables")
	if err != nil {
		return false, fmt.Errorf("list nftables tables: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "table "+tableFamily+" "+tableName {
			return true, nil
		}
	}

	return false, nil
}

func Ruleset(plan qos.ClassificationPlan) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	if !plan.Enabled {
		return "", fmt.Errorf("nftables classification plan must be enabled")
	}
	mark := fmt.Sprintf("0x%08x", plan.ArgoRule.PacketMark)
	preserveMask := fmt.Sprintf("0x%08x", ^plan.ArgoRule.MarkMask)
	var rules strings.Builder
	rules.WriteString("table ")
	rules.WriteString(tableFamily)
	rules.WriteByte(' ')
	rules.WriteString(tableName)
	rules.WriteString(" {\n chain ")
	rules.WriteString(chainName)
	rules.WriteString(" {\n  type route hook output priority mangle; policy accept;\n  oifname \"")
	rules.WriteString(plan.Interface)
	rules.WriteString("\" socket cgroupv2 level ")
	rules.WriteString(strconv.FormatUint(uint64(plan.ArgoRule.Cgroup.Level), 10))
	rules.WriteByte(' ')
	rules.WriteString(strconv.Quote(plan.ArgoRule.Cgroup.Path))
	rules.WriteString(" counter")
	rules.WriteString(" meta mark set ((meta mark & ")
	rules.WriteString(preserveMask)
	rules.WriteString(") | ")
	rules.WriteString(mark)
	rules.WriteString(")\n }\n}\n")

	return rules.String(), nil
}

func (runner commandRunner) Run(ctx context.Context, input string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, runner.path, arguments...)
	command.Stdin = strings.NewReader(input)
	var standardError bytes.Buffer
	command.Stderr = &standardError
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(standardError.String())
		if message == "" {
			return output, err
		}
		return output, errors.Join(err, fmt.Errorf("nft: %s", message))
	}

	return output, nil
}
