package qos

import (
	"context"
	"fmt"
	"sync"
)

const (
	ArgoPacketMark     uint32 = 0xA400
	ArgoPacketMarkMask uint32 = 0xFFFF
)

type TrafficClass string

const (
	TrafficClassArgo    TrafficClass = "argo"
	TrafficClassDefault TrafficClass = "default"
)

type ClassificationRule struct {
	Class      TrafficClass
	Cgroup     CgroupSelector
	PacketMark uint32
	MarkMask   uint32
}

type ClassificationPlan struct {
	Enabled      bool
	Interface    string
	ArgoRule     ClassificationRule
	DefaultClass TrafficClass
}

func GenerateClassification(interfaceName string, cgroup CgroupSelector) (ClassificationPlan, error) {
	if err := ValidateInterface(interfaceName); err != nil {
		return ClassificationPlan{}, err
	}
	if err := cgroup.Validate(); err != nil {
		return ClassificationPlan{}, err
	}

	return ClassificationPlan{
		Enabled:   true,
		Interface: interfaceName,
		ArgoRule: ClassificationRule{
			Class:      TrafficClassArgo,
			Cgroup:     cgroup,
			PacketMark: ArgoPacketMark,
			MarkMask:   ArgoPacketMarkMask,
		},
		DefaultClass: TrafficClassDefault,
	}, nil
}

func (plan ClassificationPlan) Validate() error {
	if !plan.Enabled {
		if plan != (ClassificationPlan{}) {
			return fmt.Errorf("disabled classification plan must be empty")
		}
		return nil
	}
	if err := ValidateInterface(plan.Interface); err != nil {
		return err
	}
	if plan.ArgoRule.Class != TrafficClassArgo || plan.ArgoRule.Cgroup.Validate() != nil {
		return fmt.Errorf("classification plan requires one Argo cgroup rule")
	}
	if plan.ArgoRule.PacketMark != ArgoPacketMark || plan.ArgoRule.MarkMask != ArgoPacketMarkMask {
		return fmt.Errorf("classification plan uses an unowned packet mark")
	}
	if plan.DefaultClass != TrafficClassDefault {
		return fmt.Errorf("classification plan must preserve unmatched traffic as default")
	}

	return nil
}

type ClassificationBackend interface {
	ApplyClassification(context.Context, ClassificationPlan) error
	RemoveClassification(context.Context, string) error
}

type ClassificationController struct {
	backend ClassificationBackend
	mutex   sync.Mutex
	current ClassificationPlan
	applied bool
}

func NewClassificationController(backend ClassificationBackend) (*ClassificationController, error) {
	if backend == nil {
		return nil, fmt.Errorf("classification backend is required")
	}

	return &ClassificationController{backend: backend}, nil
}

func (controller *ClassificationController) Reconcile(
	ctx context.Context,
	desired ClassificationPlan,
) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.applied && controller.current == desired {
		return nil
	}
	if !desired.Enabled {
		if !controller.applied {
			return nil
		}
		if err := controller.backend.RemoveClassification(ctx, controller.current.Interface); err != nil {
			return fmt.Errorf("remove classification from %s: %w", controller.current.Interface, err)
		}
		controller.current = ClassificationPlan{}
		controller.applied = false
		return nil
	}
	if controller.applied && controller.current.Interface != desired.Interface {
		if err := controller.backend.RemoveClassification(ctx, controller.current.Interface); err != nil {
			return fmt.Errorf("remove classification from %s: %w", controller.current.Interface, err)
		}
		controller.current = ClassificationPlan{}
		controller.applied = false
	}
	if err := controller.backend.ApplyClassification(ctx, desired); err != nil {
		return fmt.Errorf("apply classification to %s: %w", desired.Interface, err)
	}
	controller.current = desired
	controller.applied = true

	return nil
}

func (controller *ClassificationController) Current() (ClassificationPlan, bool) {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()

	return controller.current, controller.applied
}
