package tc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/kristyancarvalho/argo/internal/qos"
)

const (
	rootHandle              = "a400:"
	rootClass               = "a400:1"
	argoClass               = "a400:10"
	defaultClass            = "a400:20"
	argoQdisc               = "a410:"
	defaultQdisc            = "a420:"
	ingressMarkerPriority   = "16399"
	ingressRedirectPriority = "16400"
	shapingHeadroomPercent  = uint64(95)
)

type Command struct {
	Arguments []string
}

type Tree struct {
	Interface                string
	IFBInterface             string
	LinkRateBitsPerSecond    uint64
	ArgoRateBitsPerSecond    uint64
	ArgoCeilingBitsPerSecond uint64
	ShapingRateBitsPerSecond uint64
	DefaultRateBitsPerSecond uint64
	Commands                 []Command
}

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type Backend struct {
	runner     Runner
	linkRunner Runner
	mutex      sync.Mutex
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

type IngressConflictError struct {
	Interface string
	Priority  string
}

func (err IngressConflictError) Error() string {
	return fmt.Sprintf("interface %s has a non-Argo ingress filter at priority %s", err.Interface, err.Priority)
}

type IFBConflictError struct {
	Interface string
}

func (err IFBConflictError) Error() string {
	return fmt.Sprintf("interface %s exists but is not owned by Argo", err.Interface)
}

func (err RootConflictError) Error() string {
	return fmt.Sprintf("interface %s has a non-Argo root qdisc", err.Interface)
}

func New() (*Backend, error) {
	tcPath, err := exec.LookPath("tc")
	if err != nil {
		return nil, UnavailableError{Reason: err.Error()}
	}
	ipPath, err := exec.LookPath("ip")
	if err != nil {
		return nil, UnavailableError{Reason: err.Error()}
	}

	return NewWithRunners(commandRunner{path: tcPath}, commandRunner{path: ipPath})
}

func NewWithExecutable(name string) (*Backend, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, UnavailableError{Reason: err.Error()}
	}

	ipPath, err := exec.LookPath("ip")
	if err != nil {
		return nil, UnavailableError{Reason: err.Error()}
	}

	return NewWithRunners(commandRunner{path: path}, commandRunner{path: ipPath})
}

func NewWithRunner(runner Runner) (*Backend, error) {
	return NewWithRunners(runner, runner)
}

func NewWithRunners(runner Runner, linkRunner Runner) (*Backend, error) {
	if runner == nil || linkRunner == nil {
		return nil, fmt.Errorf("tc and ip runners are required")
	}

	return &Backend{runner: runner, linkRunner: linkRunner}, nil
}

func GenerateTree(interfaceName string, linkRate, argoRate uint64) (Tree, error) {
	return GenerateTreeWithArgoCeiling(interfaceName, linkRate, argoRate, linkRate)
}

func GenerateTreeForState(state qos.DesiredState) (Tree, error) {
	if err := state.Validate(); err != nil {
		return Tree{}, err
	}
	if !state.Enabled {
		return Tree{}, fmt.Errorf("cannot generate tc tree for disabled QoS state")
	}
	ceiling := state.LinkRateBitsPerSecond
	if state.Policy == qos.PolicyLatency || state.Policy == qos.PolicyBackground {
		ceiling = state.ArgoRateBitsPerSecond
	}

	return GenerateTreeWithArgoCeiling(
		state.Interface,
		state.LinkRateBitsPerSecond,
		state.ArgoRateBitsPerSecond,
		ceiling,
	)
}

func GenerateTreeWithArgoCeiling(interfaceName string, linkRate, argoRate, argoCeiling uint64) (Tree, error) {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return Tree{}, err
	}
	if linkRate == 0 {
		return Tree{}, fmt.Errorf("link rate must be positive")
	}
	if argoRate == 0 || argoRate >= linkRate {
		return Tree{}, fmt.Errorf("argo rate must be positive and lower than link rate")
	}
	if argoCeiling < argoRate || argoCeiling > linkRate {
		return Tree{}, fmt.Errorf("argo ceiling must be at least the Argo rate and at most the link rate")
	}
	shapingRate := applyHeadroom(linkRate)
	shapingArgoRate := applyHeadroom(argoRate)
	shapingArgoCeiling := applyHeadroom(argoCeiling)
	defaultRate := shapingRate - shapingArgoRate
	ifb := IFBInterface(interfaceName)
	link := rate(shapingRate)
	argo := rate(shapingArgoRate)
	other := rate(defaultRate)
	commands := []Command{
		{Arguments: []string{"qdisc", "replace", "dev", ifb, "root", "handle", rootHandle, "htb", "default", "20"}},
		{Arguments: []string{"class", "replace", "dev", ifb, "parent", rootHandle, "classid", rootClass, "htb", "rate", link, "ceil", link}},
		{Arguments: []string{"class", "replace", "dev", ifb, "parent", rootClass, "classid", argoClass, "htb", "rate", argo, "ceil", rate(shapingArgoCeiling)}},
		{Arguments: []string{"class", "replace", "dev", ifb, "parent", rootClass, "classid", defaultClass, "htb", "rate", other, "ceil", link}},
		{Arguments: []string{"qdisc", "replace", "dev", ifb, "parent", argoClass, "handle", argoQdisc, "fq_codel"}},
		{Arguments: []string{"qdisc", "replace", "dev", ifb, "parent", defaultClass, "handle", defaultQdisc, "fq_codel"}},
		{Arguments: []string{"filter", "replace", "dev", ifb, "parent", rootHandle, "protocol", "all", "prio", "1", "handle", fmt.Sprintf("0x%x", qos.ArgoPacketMark), "fw", "classid", argoClass}},
	}

	return Tree{
		Interface:                interfaceName,
		IFBInterface:             ifb,
		LinkRateBitsPerSecond:    linkRate,
		ArgoRateBitsPerSecond:    argoRate,
		ArgoCeilingBitsPerSecond: argoCeiling,
		ShapingRateBitsPerSecond: shapingRate,
		DefaultRateBitsPerSecond: defaultRate,
		Commands:                 commands,
	}, nil
}

func (backend *Backend) Apply(ctx context.Context, tree Tree) error {
	ceiling := tree.ArgoCeilingBitsPerSecond
	if ceiling == 0 {
		ceiling = tree.LinkRateBitsPerSecond
	}
	canonical, err := GenerateTreeWithArgoCeiling(
		tree.Interface,
		tree.LinkRateBitsPerSecond,
		tree.ArgoRateBitsPerSecond,
		ceiling,
	)
	if err != nil {
		return err
	}
	tree = canonical
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if err := backend.ensureIFB(ctx, tree); err != nil {
		return err
	}
	owned, _, err := backend.rootState(ctx, tree.IFBInterface)
	if err != nil {
		return err
	}
	commands := tree.Commands
	if owned {
		commands = commands[1:]
	}
	for _, command := range commands {
		if _, err := backend.runner.Run(ctx, command.Arguments...); err != nil {
			_ = backend.removeOwned(context.WithoutCancel(ctx), tree)
			return fmt.Errorf("apply Argo tc tree command %q: %w", strings.Join(command.Arguments, " "), err)
		}
	}
	if err := backend.ensureIngress(ctx, tree); err != nil {
		_ = backend.removeOwned(context.WithoutCancel(ctx), tree)
		return err
	}

	return nil
}

func (backend *Backend) Remove(ctx context.Context, interfaceName string) error {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return err
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	return backend.removeOwned(ctx, Tree{Interface: interfaceName, IFBInterface: IFBInterface(interfaceName)})
}

func IFBInterface(interfaceName string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(interfaceName))

	return fmt.Sprintf("argo%08x", hash.Sum32())
}

func (backend *Backend) ensureIFB(ctx context.Context, tree Tree) error {
	output, err := backend.linkRunner.Run(ctx, "-details", "-o", "link", "show", "dev", tree.IFBInterface)
	if err == nil {
		text := string(output)
		if !strings.Contains(text, " ifb ") || !strings.Contains(text, "alias "+ifbAlias(tree.Interface)) {
			return IFBConflictError{Interface: tree.IFBInterface}
		}
		_, err = backend.linkRunner.Run(ctx, "link", "set", "dev", tree.IFBInterface, "up")
		if err != nil {
			return fmt.Errorf("activate Argo IFB %s: %w", tree.IFBInterface, err)
		}

		return nil
	}
	if _, err := backend.linkRunner.Run(ctx, "link", "add", "name", tree.IFBInterface, "type", "ifb"); err != nil {
		return fmt.Errorf("create Argo IFB %s: %w", tree.IFBInterface, err)
	}
	if _, err := backend.linkRunner.Run(ctx, "link", "set", "dev", tree.IFBInterface, "alias", ifbAlias(tree.Interface)); err != nil {
		_, _ = backend.linkRunner.Run(context.WithoutCancel(ctx), "link", "delete", "dev", tree.IFBInterface)
		return fmt.Errorf("identify Argo IFB %s: %w", tree.IFBInterface, err)
	}
	if _, err := backend.linkRunner.Run(ctx, "link", "set", "dev", tree.IFBInterface, "up"); err != nil {
		_, _ = backend.linkRunner.Run(context.WithoutCancel(ctx), "link", "delete", "dev", tree.IFBInterface)
		return fmt.Errorf("activate Argo IFB %s: %w", tree.IFBInterface, err)
	}

	return nil
}

func (backend *Backend) ensureIngress(ctx context.Context, tree Tree) error {
	output, err := backend.runner.Run(ctx, "qdisc", "show", "dev", tree.Interface)
	if err != nil {
		return fmt.Errorf("inspect ingress qdisc on %s: %w", tree.Interface, err)
	}
	hasIngress := strings.Contains(string(output), "qdisc ingress ") || strings.Contains(string(output), "qdisc clsact ")
	if !hasIngress {
		if _, err := backend.runner.Run(ctx, "qdisc", "add", "dev", tree.Interface, "handle", "ffff:", "ingress"); err != nil {
			return fmt.Errorf("create ingress qdisc on %s: %w", tree.Interface, err)
		}
		if _, err := backend.runner.Run(ctx, ingressMarkerCommand(tree.Interface)...); err != nil {
			_, _ = backend.runner.Run(context.WithoutCancel(ctx), "qdisc", "del", "dev", tree.Interface, "ingress")
			return fmt.Errorf("mark Argo ingress qdisc on %s: %w", tree.Interface, err)
		}
	} else if _, err := backend.verifyFilterSlot(ctx, tree, ingressMarkerPriority, true); err != nil {
		return err
	}
	exists, err := backend.verifyFilterSlot(ctx, tree, ingressRedirectPriority, false)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := backend.runner.Run(ctx, ingressRedirectCommand(tree)...); err != nil {
		return fmt.Errorf("redirect ingress traffic from %s: %w", tree.Interface, err)
	}

	return nil
}

func (backend *Backend) verifyFilterSlot(ctx context.Context, tree Tree, priority string, marker bool) (bool, error) {
	output, err := backend.runner.Run(ctx, "filter", "show", "dev", tree.Interface, "ingress", "pref", priority)
	if err != nil {
		return false, fmt.Errorf("inspect ingress filter %s on %s: %w", priority, tree.Interface, err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, nil
	}
	text := string(output)
	owned := strings.Contains(text, "matchall")
	if marker {
		owned = owned && strings.Contains(text, "gact action continue")
	} else {
		owned = owned && strings.Contains(text, "ctinfo") && strings.Contains(text, "Redirect to device "+tree.IFBInterface)
	}
	if !owned {
		return false, IngressConflictError{Interface: tree.Interface, Priority: priority}
	}

	return true, nil
}

func (backend *Backend) removeOwned(ctx context.Context, tree Tree) error {
	var removalErrors []error
	qdiscs, qdiscError := backend.runner.Run(ctx, "qdisc", "show", "dev", tree.Interface)
	if qdiscError == nil && (strings.Contains(string(qdiscs), "qdisc ingress ") || strings.Contains(string(qdiscs), "qdisc clsact ")) {
		redirect, err := backend.filterOwned(ctx, tree, ingressRedirectPriority, false)
		if err != nil {
			removalErrors = append(removalErrors, err)
		} else if redirect {
			if _, err := backend.runner.Run(ctx, ingressDeleteCommand(tree.Interface, ingressRedirectPriority)...); err != nil {
				removalErrors = append(removalErrors, fmt.Errorf("remove Argo ingress redirect: %w", err))
			}
		}
		marker, err := backend.filterOwned(ctx, tree, ingressMarkerPriority, true)
		if err != nil {
			removalErrors = append(removalErrors, err)
		} else if marker {
			if _, err := backend.runner.Run(ctx, ingressDeleteCommand(tree.Interface, ingressMarkerPriority)...); err != nil {
				removalErrors = append(removalErrors, fmt.Errorf("remove Argo ingress marker: %w", err))
			} else {
				filters, filterError := backend.runner.Run(ctx, "filter", "show", "dev", tree.Interface, "ingress")
				if filterError != nil {
					removalErrors = append(removalErrors, fmt.Errorf("inspect remaining ingress filters: %w", filterError))
				} else if len(bytes.TrimSpace(filters)) == 0 {
					if _, err := backend.runner.Run(ctx, "qdisc", "del", "dev", tree.Interface, "ingress"); err != nil {
						removalErrors = append(removalErrors, fmt.Errorf("remove Argo ingress qdisc: %w", err))
					}
				}
			}
		}
	}
	if err := backend.removeIFB(ctx, tree); err != nil {
		removalErrors = append(removalErrors, err)
	}

	return errors.Join(removalErrors...)
}

func (backend *Backend) filterOwned(ctx context.Context, tree Tree, priority string, marker bool) (bool, error) {
	output, err := backend.runner.Run(ctx, "filter", "show", "dev", tree.Interface, "ingress", "pref", priority)
	if err != nil {
		if backend.ifbMissing(ctx, tree.IFBInterface) {
			return false, nil
		}
		return false, fmt.Errorf("inspect Argo ingress filter: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, nil
	}
	text := string(output)
	if marker {
		return strings.Contains(text, "matchall") && strings.Contains(text, "gact action continue"), nil
	}

	return strings.Contains(text, "matchall") && strings.Contains(text, "ctinfo") && strings.Contains(text, "Redirect to device "+tree.IFBInterface), nil
}

func (backend *Backend) removeIFB(ctx context.Context, tree Tree) error {
	output, err := backend.linkRunner.Run(ctx, "-details", "-o", "link", "show", "dev", tree.IFBInterface)
	if err != nil {
		return nil
	}
	text := string(output)
	if !strings.Contains(text, " ifb ") || !strings.Contains(text, "alias "+ifbAlias(tree.Interface)) {
		return nil
	}
	if _, err := backend.linkRunner.Run(ctx, "link", "delete", "dev", tree.IFBInterface); err != nil {
		return fmt.Errorf("remove Argo IFB %s: %w", tree.IFBInterface, err)
	}

	return nil
}

func (backend *Backend) ifbMissing(ctx context.Context, name string) bool {
	_, err := backend.linkRunner.Run(ctx, "-details", "-o", "link", "show", "dev", name)

	return err != nil
}

func ingressMarkerCommand(interfaceName string) []string {
	return []string{"filter", "add", "dev", interfaceName, "ingress", "protocol", "all", "pref", ingressMarkerPriority, "handle", "1", "matchall", "action", "gact", "continue"}
}

func ingressRedirectCommand(tree Tree) []string {
	return []string{"filter", "add", "dev", tree.Interface, "ingress", "protocol", "all", "pref", ingressRedirectPriority, "handle", "1", "matchall", "action", "ctinfo", "cpmark", fmt.Sprintf("0x%08x", qos.ArgoPacketMarkMask), "pipe", "action", "mirred", "egress", "redirect", "dev", tree.IFBInterface}
}

func ingressDeleteCommand(interfaceName, priority string) []string {
	return []string{"filter", "del", "dev", interfaceName, "ingress", "protocol", "all", "pref", priority, "handle", "1", "matchall"}
}

func ifbAlias(interfaceName string) string {
	return "argo-rx:" + interfaceName
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

func applyHeadroom(bits uint64) uint64 {
	if bits < 100 {
		return bits
	}

	return bits/100*shapingHeadroomPercent + bits%100*shapingHeadroomPercent/100
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
