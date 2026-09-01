package tc

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
	rootHandle   = "a400:"
	rootClass    = "a400:1"
	argoClass    = "a400:10"
	defaultClass = "a400:20"
	argoQdisc    = "a410:"
	defaultQdisc = "a420:"
)

type Command struct {
	Arguments []string
}

type Tree struct {
	Interface                string
	LinkRateBitsPerSecond    uint64
	ArgoRateBitsPerSecond    uint64
	DefaultRateBitsPerSecond uint64
	Commands                 []Command
}

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
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
	return "tc is unavailable: " + err.Reason
}

type RootConflictError struct {
	Interface string
}

func (err RootConflictError) Error() string {
	return fmt.Sprintf("interface %s has a non-Argo root qdisc", err.Interface)
}

func New() (*Backend, error) {
	return NewWithExecutable("tc")
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
		return nil, fmt.Errorf("tc runner is required")
	}

	return &Backend{runner: runner}, nil
}

func GenerateTree(interfaceName string, linkRate, argoRate uint64) (Tree, error) {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return Tree{}, err
	}
	if linkRate == 0 {
		return Tree{}, fmt.Errorf("link rate must be positive")
	}
	if argoRate == 0 || argoRate >= linkRate {
		return Tree{}, fmt.Errorf("argo rate must be positive and lower than link rate")
	}
	defaultRate := linkRate - argoRate
	link := rate(linkRate)
	argo := rate(argoRate)
	other := rate(defaultRate)
	commands := []Command{
		{Arguments: []string{"qdisc", "replace", "dev", interfaceName, "root", "handle", rootHandle, "htb", "default", "20"}},
		{Arguments: []string{"class", "replace", "dev", interfaceName, "parent", rootHandle, "classid", rootClass, "htb", "rate", link, "ceil", link}},
		{Arguments: []string{"class", "replace", "dev", interfaceName, "parent", rootClass, "classid", argoClass, "htb", "rate", argo, "ceil", link}},
		{Arguments: []string{"class", "replace", "dev", interfaceName, "parent", rootClass, "classid", defaultClass, "htb", "rate", other, "ceil", link}},
		{Arguments: []string{"qdisc", "replace", "dev", interfaceName, "parent", argoClass, "handle", argoQdisc, "fq_codel"}},
		{Arguments: []string{"qdisc", "replace", "dev", interfaceName, "parent", defaultClass, "handle", defaultQdisc, "fq_codel"}},
		{Arguments: []string{"filter", "replace", "dev", interfaceName, "parent", rootHandle, "protocol", "all", "prio", "1", "handle", fmt.Sprintf("0x%x", qos.ArgoPacketMark), "fw", "classid", argoClass}},
	}

	return Tree{
		Interface:                interfaceName,
		LinkRateBitsPerSecond:    linkRate,
		ArgoRateBitsPerSecond:    argoRate,
		DefaultRateBitsPerSecond: defaultRate,
		Commands:                 commands,
	}, nil
}

func (backend *Backend) Apply(ctx context.Context, tree Tree) error {
	canonical, err := GenerateTree(tree.Interface, tree.LinkRateBitsPerSecond, tree.ArgoRateBitsPerSecond)
	if err != nil {
		return err
	}
	tree = canonical
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	owned, conflict, err := backend.rootState(ctx, tree.Interface)
	if err != nil {
		return err
	}
	if conflict {
		return RootConflictError{Interface: tree.Interface}
	}
	commands := tree.Commands
	if owned {
		commands = commands[1:]
	}
	for _, command := range commands {
		if _, err := backend.runner.Run(ctx, command.Arguments...); err != nil {
			_, _ = backend.runner.Run(context.WithoutCancel(ctx), "qdisc", "del", "dev", tree.Interface, "root", "handle", rootHandle)
			return fmt.Errorf("apply Argo tc tree command %q: %w", strings.Join(command.Arguments, " "), err)
		}
	}

	return nil
}

func (backend *Backend) Remove(ctx context.Context, interfaceName string) error {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return err
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	owned, _, err := backend.rootState(ctx, interfaceName)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}
	if _, err := backend.runner.Run(ctx, "qdisc", "del", "dev", interfaceName, "root", "handle", rootHandle); err != nil {
		return fmt.Errorf("remove Argo tc tree: %w", err)
	}

	return nil
}

func (backend *Backend) rootState(ctx context.Context, interfaceName string) (bool, bool, error) {
	output, err := backend.runner.Run(ctx, "qdisc", "show", "dev", interfaceName)
	if err != nil {
		return false, false, fmt.Errorf("inspect tc qdiscs on %s: %w", interfaceName, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, " root ") {
			continue
		}
		if strings.Contains(line, "qdisc htb "+rootHandle) {
			return true, false, nil
		}
		if strings.Contains(line, "qdisc noqueue ") {
			return false, false, nil
		}
		return false, true, nil
	}

	return false, false, nil
}

func rate(bits uint64) string {
	return strconv.FormatUint(bits, 10) + "bit"
}

func (runner commandRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, runner.path, arguments...)
	var standardError bytes.Buffer
	command.Stderr = &standardError
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(standardError.String())
		if message == "" {
			return output, err
		}
		return output, errors.Join(err, fmt.Errorf("tc: %s", message))
	}

	return output, nil
}
