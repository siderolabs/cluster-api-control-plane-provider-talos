// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package runtimeclient calls Cluster API runtime extensions.
//
// Cluster API exports the runtime client *interface* from exp/runtime/client but keeps its
// implementation in internal/runtime/client, so an out-of-tree provider cannot construct
// one. This package supplies the small slice that the in-place update flow needs: discovering
// which extension serves a hook, and calling it.
package runtimeclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
)

// Caller is the slice of the Cluster API runtime client that in-place updates need.
//
// Kept narrow deliberately: it is the seam the control plane reconciler depends on, so tests
// substitute a fake instead of standing up an extension server.
type Caller interface {
	// GetAllExtensions returns the names of the extension handlers registered for a hook.
	GetAllExtensions(ctx context.Context, hook runtimecatalog.Hook, forObject metav1.Object) ([]string, error)

	// CallExtension calls a named extension handler.
	CallExtension(ctx context.Context, hook runtimecatalog.Hook, forObject metav1.Object, name string, request, response any) error
}

// Client discovers and calls runtime extensions via ExtensionConfig resources.
type Client struct {
	reader  client.Reader
	catalog *runtimecatalog.Catalog
}

// New builds a Client reading ExtensionConfigs through reader.
func New(reader client.Reader) *Client {
	catalog := runtimecatalog.New()
	_ = runtimehooksv1.AddToCatalog(catalog) //nolint:errcheck // the built-in catalog cannot fail

	return &Client{reader: reader, catalog: catalog}
}

// handlerFor locates the ExtensionConfig and handler serving a hook.
func (c *Client) handlerFor(ctx context.Context, hook runtimecatalog.Hook, name string) (*runtimev1.ExtensionConfig, *runtimev1.ExtensionHandler, error) {
	gvh, err := c.catalog.GroupVersionHook(hook)
	if err != nil {
		return nil, nil, fmt.Errorf("hook is not in the catalog: %w", err)
	}

	var list runtimev1.ExtensionConfigList
	if err := c.reader.List(ctx, &list); err != nil {
		return nil, nil, fmt.Errorf("failed to list ExtensionConfigs: %w", err)
	}

	for i := range list.Items {
		config := &list.Items[i]

		for j := range config.Status.Handlers {
			handler := &config.Status.Handlers[j]

			if handler.RequestHook.Hook != gvh.Hook || handler.RequestHook.APIVersion != gvh.GroupVersion().String() {
				continue
			}

			if name != "" && handler.Name != name {
				continue
			}

			return config, handler, nil
		}
	}

	return nil, nil, nil
}

// GetAllExtensions returns the names of handlers registered for a hook.
func (c *Client) GetAllExtensions(ctx context.Context, hook runtimecatalog.Hook, _ metav1.Object) ([]string, error) {
	gvh, err := c.catalog.GroupVersionHook(hook)
	if err != nil {
		return nil, fmt.Errorf("hook is not in the catalog: %w", err)
	}

	var list runtimev1.ExtensionConfigList
	if err := c.reader.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("failed to list ExtensionConfigs: %w", err)
	}

	var names []string

	for i := range list.Items {
		for _, handler := range list.Items[i].Status.Handlers {
			if handler.RequestHook.Hook == gvh.Hook && handler.RequestHook.APIVersion == gvh.GroupVersion().String() {
				names = append(names, handler.Name)
			}
		}
	}

	return names, nil
}

// CallExtension posts request to the named handler and decodes the reply into response.
func (c *Client) CallExtension(ctx context.Context, hook runtimecatalog.Hook, _ metav1.Object, name string, request, response any) error {
	config, handler, err := c.handlerFor(ctx, hook, name)
	if err != nil {
		return err
	}

	if handler == nil {
		return fmt.Errorf("no extension handler %q registered for the hook", name)
	}

	gvh, err := c.catalog.GroupVersionHook(hook)
	if err != nil {
		return fmt.Errorf("hook is not in the catalog: %w", err)
	}

	endpoint, err := endpointFor(config, handler, gvh)
	if err != nil {
		return err
	}

	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to encode hook request: %w", err)
	}

	timeout := time.Duration(handler.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	httpClient, err := clientFor(config)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call extension %q: %w", name, err)
	}

	defer resp.Body.Close() //nolint:errcheck // response body close on a read path

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("extension %q returned status %s", name, resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("failed to decode reply from extension %q: %w", name, err)
	}

	return nil
}

// endpointFor builds the URL of a handler from its ExtensionConfig client config.
//
// The path is built with the catalog's own helper rather than by hand. A runtime extension server
// registers each handler under /<group>/<version>/<hook>/<name>, all lower cased, so a URL made
// from the handler name alone reaches the server and comes back 404.
func endpointFor(config *runtimev1.ExtensionConfig, handler *runtimev1.ExtensionHandler, gvh runtimecatalog.GroupVersionHook) (string, error) {
	clientConfig := config.Spec.ClientConfig

	// Discovery reports a handler as "<name>.<ExtensionConfig name>", but the server registers it
	// under the bare name it was added with, so the suffix has to come back off before building
	// the path. Core Cluster API does the same before calling.
	name := strings.TrimSuffix(handler.Name, "."+config.Name)

	hookPath := runtimecatalog.GVHToPath(gvh, name)

	switch {
	case clientConfig.URL != "":
		base, err := url.Parse(clientConfig.URL)
		if err != nil {
			return "", fmt.Errorf("invalid extension URL %q: %w", clientConfig.URL, err)
		}

		base.Path = path.Join(base.Path, hookPath)

		return base.String(), nil

	case clientConfig.Service.Name != "":
		svc := clientConfig.Service

		port := int32(443)
		if svc.Port != nil {
			port = *svc.Port
		}

		host := fmt.Sprintf("%s.%s.svc:%d", svc.Name, svc.Namespace, port)

		return (&url.URL{
			Scheme: "https",
			Host:   host,
			Path:   path.Join(svc.Path, hookPath),
		}).String(), nil

	default:
		return "", fmt.Errorf("ExtensionConfig %s specifies neither url nor service", config.Name)
	}
}

// clientFor builds an HTTPS client trusting the CA bundle the ExtensionConfig advertises.
func clientFor(config *runtimev1.ExtensionConfig) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if len(config.Spec.ClientConfig.CABundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(config.Spec.ClientConfig.CABundle) {
			return nil, fmt.Errorf("ExtensionConfig %s has an unparseable caBundle", config.Name)
		}

		tlsConfig.RootCAs = pool
	}

	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}, nil
}
