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
	machineapi "github.com/talos-systems/talos/pkg/machinery/api/machine"
	talosclient "github.com/talos-systems/talos/pkg/machinery/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	capiv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"
)

type errServiceUnhealthy struct {
	service string
	reason  string
}

func (e *errServiceUnhealthy) Error() string {
	return fmt.Sprintf("Service %s is unhealthy: %s", e.service, e.reason)
}

func (r *TalosControlPlaneReconciler) nodesHealthcheck(ctx context.Context, cluster *capiv1.Cluster, machines []capiv1.Machine) error {
	clientset, err := r.kubeconfigForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		return err
	}

	client, err := r.talosconfigForMachines(ctx, clientset, machines...)
	if err != nil {
		return err
	}

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
}

func (r *TalosControlPlaneReconciler) ensureNodesBooted(ctx context.Context, cluster *clusterv1.Cluster, machines []clusterv1.Machine) error {
	clientset, err := r.kubeconfigForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		return err
	}

	client, err := r.talosconfigForMachines(ctx, clientset, machines...)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	nodesBootStarted := map[string]struct{}{}
	nodesBootStopped := map[string]struct{}{}

	err = client.EventsWatch(ctx, func(ch <-chan talosclient.Event) {
		defer cancel()

		for event := range ch {
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
	}, talosclient.WithTailEvents(-1))

	if err != nil {
		unwrappedErr := err

		for {
			if s, ok := status.FromError(unwrappedErr); ok && s.Code() == codes.DeadlineExceeded {
				// ignore deadline exceeded as we've just exhausted events list
				err = nil

				break
			}

			unwrappedErr = errors.Unwrap(unwrappedErr)
			if unwrappedErr == nil {
				break
			}
		}
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		return err
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
