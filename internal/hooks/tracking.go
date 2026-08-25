// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package hooks tracks pending Cluster API runtime hooks on an object.
//
// This is a minimal port of sigs.k8s.io/cluster-api/internal/hooks, which is not importable
// from out-of-tree providers. The annotation format is part of the contract between the
// controller that owns a Machine and the core Machine controller, so it has to match
// upstream exactly.
//
// Derived from Cluster API, Copyright The Kubernetes Authors, Apache License 2.0.
package hooks

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
)

// IsPending returns true if the given hook is tracked as pending on obj.
func IsPending(hook runtimecatalog.Hook, obj client.Object) bool {
	return isInList(obj.GetAnnotations()[runtimev1.PendingHooksAnnotation], runtimecatalog.HookName(hook))
}

// MarkAsPending records the intent to call hooks on obj and patches it.
//
// Writing this annotation is what releases the core Machine controller to begin calling the
// UpdateMachine hook, so it must be the last step of triggering an in-place update.
func MarkAsPending(ctx context.Context, c client.Client, obj client.Object, hooks ...runtimecatalog.Hook) error {
	names := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		names = append(names, runtimecatalog.HookName(hook))
	}

	orig := obj.DeepCopyObject().(client.Object) //nolint:errcheck,forcetypeassert // client.Object round-trips

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	updated := addToList(annotations[runtimev1.PendingHooksAnnotation], names...)
	if annotations[runtimev1.PendingHooksAnnotation] == updated {
		return nil
	}

	annotations[runtimev1.PendingHooksAnnotation] = updated
	obj.SetAnnotations(annotations)

	if err := c.Patch(ctx, obj, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to mark hook(s) %s as pending: %w", strings.Join(names, ","), err)
	}

	return nil
}

func addToList(list string, items ...string) string {
	set := sets.New[string]()
	if list != "" {
		set.Insert(strings.Split(list, ",")...)
	}

	set.Insert(items...)

	return strings.Join(sets.List(set), ",")
}

func isInList(list, item string) bool {
	if list == "" {
		return false
	}

	return sets.New(strings.Split(list, ",")...).Has(item)
}
