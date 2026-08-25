// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package ssa wraps server-side apply for the control plane provider.
//
// Cluster API's own helper lives under internal/ and is not importable from out-of-tree
// providers, so this supplies the small piece that in-place updates need.
package ssa

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ManagerName is the field manager the control plane provider applies as.
const ManagerName = "capi-taloscontrolplane"

// Patch server-side applies obj, taking ownership of the fields it sets.
func Patch(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Patch(ctx, obj, client.Apply, client.FieldOwner(ManagerName), client.ForceOwnership); err != nil {
		return fmt.Errorf("server-side apply failed for %s: %w", client.ObjectKeyFromObject(obj), err)
	}

	return nil
}

// DryRunPatch server-side applies obj without persisting it, returning the object as the
// API server would store it.
//
// In-place update planning uses this to normalise both the current and the desired objects
// before diffing them, so that defaulting and admission do not show up as spurious changes
// and needlessly force a rolling replacement.
func DryRunPatch(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Patch(ctx, obj, client.Apply,
		client.FieldOwner(ManagerName),
		client.ForceOwnership,
		client.DryRunAll,
	); err != nil {
		return fmt.Errorf("server-side apply dry-run failed for %s: %w", client.ObjectKeyFromObject(obj), err)
	}

	return nil
}
