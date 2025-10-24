// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pkg/errors"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	"google.golang.org/grpc/codes"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

type errServiceUnhealthy struct {
	service string
	reason  string
}

func (e *errServiceUnhealthy) Error() string {
	return fmt.Sprintf("Service %s is unhealthy: %s", e.service, e.reason)
}

func (r *TalosControlPlaneReconciler) nodesHealthcheck(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	for _, machine := range machines {
		if err := func() error {
			client, err := r.talosconfigForMachines(ctx, tcp, machine)
			if err != nil {
				return err
			}

			defer client.Close() //nolint:errcheck

			serviceList, err := client.ServiceList(ctx)
			if err != nil {
				return err
			}

			for _, message := range serviceList.Messages {
				for _, svc := range message.Services {
					if !svc.GetHealth().Unknown && !svc.GetHealth().Healthy {
						return &errServiceUnhealthy{
							service: svc.GetId(),
							reason:  svc.GetState(),
						}
					}
				}
			}

			return nil
		}(); err != nil {
			return fmt.Errorf("machine %q: %w", machine.Name, err)
		}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) ensureNodesBooted(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	eventsCh := make(chan talosclient.EventResult)

	var clients []*talosclient.Client

	defer func() {
		for _, client := range clients {
			client.Close() //nolint:errcheck
		}
	}()

	// watch events from all machines into a single channel
	for _, machine := range machines {
		client, err := r.talosconfigForMachines(ctx, tcp, machine)
		if err != nil {
			return err
		}

		clients = append(clients, client)

		if err := client.EventsWatchV2(ctx, eventsCh, talosclient.WithTailEvents(-1)); err != nil {
			return err
		}
	}

	nodesBootStarted := map[string]struct{}{}
	nodesBootStopped := map[string]struct{}{}

loop:
	for {
		var eventResult talosclient.EventResult

		select {
		case <-ctx.Done():
			// timeout
			break loop
		case eventResult = <-eventsCh:
		}

		if eventResult.Error != nil {
			switch {
			case talosclient.StatusCode(eventResult.Error) == codes.DeadlineExceeded:
				// expected, we've exhausted events list
			case errors.Is(eventResult.Error, context.Canceled):
				// expected, fine
			default:
				return fmt.Errorf("failed to watch events: %w", eventResult.Error)
			}
		} else {
			event := eventResult.Event

			if msg, ok := event.Payload.(*machineapi.SequenceEvent); ok {
				if msg.GetSequence() == "boot" { // can't use runtime constants as they're in `internal/`
					switch msg.GetAction() { //nolint:exhaustive
					case machineapi.SequenceEvent_START:
						nodesBootStarted[event.Node] = struct{}{}
					case machineapi.SequenceEvent_STOP:
						nodesBootStopped[event.Node] = struct{}{}
					}
				}
			}
		}
	}

	nodesNotFinishedBooting := []string{}

	// check for nodes which have Boot/Start event, but no Boot/Stop even
	// if the node is up long enough, Boot/Start even might get out of the window,
	// so we can't check such nodes reliably
	for node := range nodesBootStarted {
		if _, ok := nodesBootStopped[node]; !ok {
			nodesNotFinishedBooting = append(nodesNotFinishedBooting, node)
		}
	}

	sort.Strings(nodesNotFinishedBooting)

	if len(nodesNotFinishedBooting) > 0 {
		return fmt.Errorf("nodes %q are still in boot sequence", nodesNotFinishedBooting)
	}

	return nil
}
