package qosbackend

import (
	"context"
	"errors"
	"fmt"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/nft"
	"github.com/kristyancarvalho/argo/internal/qos/tc"
)

type Backend struct{}

func New() *Backend {
	return &Backend{}
}

func (backend *Backend) Apply(ctx context.Context, state qos.DesiredState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if !state.Enabled {
		return fmt.Errorf("cannot apply disabled QoS state")
	}
	classification, err := qos.GenerateClassification(state.Interface, state.Cgroup)
	if err != nil {
		return err
	}
	tree, err := tc.GenerateTree(
		state.Interface,
		state.LinkRateBitsPerSecond,
		state.ArgoRateBitsPerSecond,
	)
	if err != nil {
		return err
	}
	nftBackend, err := nft.New()
	if err != nil {
		return err
	}
	tcBackend, err := tc.New()
	if err != nil {
		return err
	}
	if err := nftBackend.ApplyClassification(ctx, classification); err != nil {
		return err
	}
	if err := tcBackend.Apply(ctx, tree); err != nil {
		cleanupError := nftBackend.RemoveClassification(context.WithoutCancel(ctx), state.Interface)
		return errors.Join(err, cleanupError)
	}

	return nil
}

func (backend *Backend) Remove(ctx context.Context, interfaceName string) error {
	if err := qos.ValidateInterface(interfaceName); err != nil {
		return err
	}
	var removalErrors []error
	tcBackend, err := tc.New()
	if err != nil {
		removalErrors = append(removalErrors, err)
	} else if err := tcBackend.Remove(ctx, interfaceName); err != nil {
		removalErrors = append(removalErrors, err)
	}
	nftBackend, err := nft.New()
	if err != nil {
		removalErrors = append(removalErrors, err)
	} else if err := nftBackend.RemoveClassification(ctx, interfaceName); err != nil {
		removalErrors = append(removalErrors, err)
	}

	return errors.Join(removalErrors...)
}
