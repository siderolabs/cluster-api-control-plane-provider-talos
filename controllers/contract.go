// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// contractAPIVersionTimeout bounds how long the conversion to v1alpha3 waits for the apiVersion of a reference.
const contractAPIVersionTimeout = 10 * time.Second

// ContractAPIVersionGetter returns a function that resolves the apiVersion of a kind with ContractAPIVersion, for the
// conversion to v1alpha3. It reads through the cached reader, and through the uncached one until the cache is
// started: the webhooks are served before the caches are started. A failure is logged, as the conversion degrades
// to an empty apiVersion instead of failing.
func ContractAPIVersionGetter(cached, uncached client.Reader, log logr.Logger) func(schema.GroupKind) (string, error) {
	return func(gk schema.GroupKind) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), contractAPIVersionTimeout)
		defer cancel()

		apiVersion, err := ContractAPIVersion(ctx, cached, gk)
		if errors.As(err, new(*cache.ErrCacheNotStarted)) {
			apiVersion, err = ContractAPIVersion(ctx, uncached, gk)
		}

		if err != nil {
			log.Info("failed to resolve the apiVersion of a reference, it is left empty in v1alpha3", "groupKind", gk.String(), "error", err)
		}

		return apiVersion, err
	}
}

// ContractAPIVersion returns the apiVersion of a kind that the contract labels of its CRD map to the contract of
// the Cluster API version this provider is built with, the way Cluster API resolves a ContractVersionedObjectReference.
func ContractAPIVersion(ctx context.Context, c client.Reader, gk schema.GroupKind) (string, error) {
	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))

	if err := c.Get(ctx, client.ObjectKey{Name: contract.CalculateCRDName(gk.Group, gk.Kind)}, crd); err != nil {
		return "", errors.Wrapf(err, "failed to get the CRD of kind %s", gk.Kind)
	}

	apiVersion, ok := latestContractAPIVersion(crd.GetLabels())
	if !ok {
		return "", errors.Errorf("CRD %s has no contract version labels", crd.GetName())
	}

	return schema.GroupVersion{Group: gk.Group, Version: apiVersion}.String(), nil
}

// latestContractAPIVersion returns the latest apiVersion that a CRD maps to the contract of the Cluster API version
// this provider is built with, falling back to the v1beta1 contract that v1beta2 is still compatible with.
func latestContractAPIVersion(crdLabels map[string]string) (string, bool) {
	for _, contractVersion := range []string{clusterv1.GroupVersion.Version, "v1beta1"} {
		apiVersions := crdLabels[clusterv1.GroupVersion.Group+"/"+contractVersion]
		if apiVersions == "" {
			continue
		}

		versions := strings.Split(apiVersions, "_")
		slices.SortFunc(versions, version.CompareKubeAwareVersionStrings)

		return versions[len(versions)-1], true
	}

	return "", false
}
