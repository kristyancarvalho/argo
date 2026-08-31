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
	Enabled   bool   `json:"enabled"`
	Policy    Policy `json:"policy"`
	Interface string `json:"interface"`
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
		if state.Policy != PolicyOff || state.Interface != "" {
			return fmt.Errorf("disabled QoS state must use off policy without an interface")
		}
		return nil
	}
	if state.Policy == PolicyOff {
		return fmt.Errorf("enabled QoS state cannot use off policy")
	}

	return validateInterface(state.Interface)
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
