package qos

import (
	"fmt"
	"strings"
)

const maximumInterfaceNameLength = 15

type Policy string

const (
	PolicyOff        Policy = "off"
	PolicyBalanced   Policy = "balanced"
	PolicyThroughput Policy = "throughput"
	PolicyLatency    Policy = "latency"
	PolicyFocus      Policy = "focus"
)

type Intent struct {
	Policy    Policy
	Interface string
}

type DesiredState struct {
	Enabled               bool   `json:"enabled"`
	Policy                Policy `json:"policy"`
	Interface             string `json:"interface"`
	LinkRateBitsPerSecond uint64 `json:"link_rate_bits_per_second,omitempty"`
	ArgoRateBitsPerSecond uint64 `json:"argo_rate_bits_per_second,omitempty"`
	CgroupID              uint64 `json:"cgroup_id,omitempty"`
}

type PolicyEnvironment struct {
	Interface             string
	LinkRateBitsPerSecond uint64
	CgroupID              uint64
	ActiveDownloads       int
}

func ParsePolicy(value string) (Policy, error) {
	policy := Policy(value)
	if err := policy.Validate(); err != nil {
		return "", err
	}

	return policy, nil
}

func (policy Policy) Validate() error {
	switch policy {
	case PolicyOff, PolicyBalanced, PolicyThroughput, PolicyLatency, PolicyFocus:
		return nil
	default:
		return fmt.Errorf("invalid QoS policy %q", policy)
	}
}

func CalculateDesiredState(intent Intent) (DesiredState, error) {
	if err := intent.Policy.Validate(); err != nil {
		return DesiredState{}, err
	}
	if intent.Policy == PolicyOff {
		return DesiredState{Policy: PolicyOff}, nil
	}
	if err := validateInterface(intent.Interface); err != nil {
		return DesiredState{}, err
	}

	return DesiredState{
		Enabled:   true,
		Policy:    intent.Policy,
		Interface: intent.Interface,
	}, nil
}

func (state DesiredState) Validate() error {
	if err := state.Policy.Validate(); err != nil {
		return err
	}
	if !state.Enabled {
		if state.Policy != PolicyOff || state.Interface != "" || state.LinkRateBitsPerSecond != 0 ||
			state.ArgoRateBitsPerSecond != 0 || state.CgroupID != 0 {
			return fmt.Errorf("disabled QoS state must contain only the off policy")
		}
		return nil
	}
	if state.Policy == PolicyOff {
		return fmt.Errorf("enabled QoS state cannot use off policy")
	}
	if err := validateInterface(state.Interface); err != nil {
		return err
	}
	configured := state.LinkRateBitsPerSecond != 0 || state.ArgoRateBitsPerSecond != 0 || state.CgroupID != 0
	if configured && (state.LinkRateBitsPerSecond == 0 ||
		state.ArgoRateBitsPerSecond == 0 ||
		state.ArgoRateBitsPerSecond >= state.LinkRateBitsPerSecond ||
		state.CgroupID == 0) {
		return fmt.Errorf("enabled QoS shaping fields must define valid link rate, Argo rate, and cgroup")
	}

	return nil
}

func MapPolicy(policy Policy, environment PolicyEnvironment) (DesiredState, error) {
	if err := policy.Validate(); err != nil {
		return DesiredState{}, err
	}
	if policy == PolicyOff || environment.ActiveDownloads == 0 {
		return DesiredState{Policy: PolicyOff}, nil
	}
	if environment.ActiveDownloads < 0 {
		return DesiredState{}, fmt.Errorf("active download count must not be negative")
	}
	if err := validateInterface(environment.Interface); err != nil {
		return DesiredState{}, err
	}
	if environment.LinkRateBitsPerSecond < 2 || environment.CgroupID == 0 {
		return DesiredState{}, fmt.Errorf("policy requires configured link rate and Argo cgroup")
	}
	var percentage uint64
	switch policy {
	case PolicyBalanced:
		percentage = 50
	case PolicyThroughput:
		percentage = 80
	case PolicyFocus:
		percentage = 20
	case PolicyLatency:
		return DesiredState{}, fmt.Errorf("latency policy requires the adaptive controller")
	case PolicyOff:
		return DesiredState{Policy: PolicyOff}, nil
	}
	argoRate := environment.LinkRateBitsPerSecond/100*percentage +
		environment.LinkRateBitsPerSecond%100*percentage/100
	state := DesiredState{
		Enabled:               true,
		Policy:                policy,
		Interface:             environment.Interface,
		LinkRateBitsPerSecond: environment.LinkRateBitsPerSecond,
		ArgoRateBitsPerSecond: argoRate,
		CgroupID:              environment.CgroupID,
	}
	if err := state.Validate(); err != nil {
		return DesiredState{}, err
	}

	return state, nil
}

func validateInterface(name string) error {
	if name == "" {
		return fmt.Errorf("QoS interface is required")
	}
	if len(name) > maximumInterfaceNameLength {
		return fmt.Errorf("QoS interface %q exceeds %d bytes", name, maximumInterfaceNameLength)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid QoS interface %q", name)
	}
	if strings.IndexFunc(name, func(character rune) bool {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') {
			return false
		}

		return !strings.ContainsRune("_-.:", character)
	}) >= 0 {
		return fmt.Errorf("invalid QoS interface %q", name)
	}

	return nil
}

func ValidateInterface(name string) error {
	return validateInterface(name)
}
