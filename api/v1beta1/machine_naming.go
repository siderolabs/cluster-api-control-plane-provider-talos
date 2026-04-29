// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"bytes"
	"text/template"

	"github.com/pkg/errors"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
)

const (
	defaultTalosControlPlaneMachineNameTemplate = "{{ .talosControlPlane.name }}-{{ .random }}"
	maxNameLength                               = 63
	randomLength                                = 5
	maxGeneratedNameLength                      = maxNameLength - randomLength
)

// MachineNamingStrategy allows changing the naming pattern used when creating Machines.
// InfraMachines and bootstrap configs use the same name as the corresponding Machine.
type MachineNamingStrategy struct {
	// Template defines the template to use for generating the names of control plane Machine objects.
	// If not defined, it falls back to `{{ .talosControlPlane.name }}-{{ .random }}`.
	// If the generated name string exceeds 63 characters, it is trimmed to 58 characters and
	// concatenated with a random suffix of length 5.
	// The template allows the following variables:
	// * `.cluster.name`: the Cluster name.
	// * `.talosControlPlane.name`: the TalosControlPlane name.
	// * `.random`: a random alphanumeric string without vowels of length 5.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Template string `json:"template,omitempty"`
}

func (s *TalosControlPlaneSpec) GenerateMachineName(clusterName, talosControlPlaneName string) (string, error) {
	return generateTalosControlPlaneMachineName(s.MachineNamingStrategy, clusterName, talosControlPlaneName)
}

func (s *TalosControlPlaneTemplateResourceSpec) GenerateMachineName(clusterName, talosControlPlaneName string) (string, error) {
	return generateTalosControlPlaneMachineName(s.MachineNamingStrategy, clusterName, talosControlPlaneName)
}

func generateTalosControlPlaneMachineName(strategy *MachineNamingStrategy, clusterName, talosControlPlaneName string) (string, error) {
	return renderTalosControlPlaneMachineName(strategy, clusterName, talosControlPlaneName, utilrand.String(randomLength))
}

func renderTalosControlPlaneMachineName(strategy *MachineNamingStrategy, clusterName, talosControlPlaneName, random string) (string, error) {
	templateString := defaultTalosControlPlaneMachineNameTemplate
	if strategy != nil && strategy.Template != "" {
		templateString = strategy.Template
	}

	data := map[string]interface{}{
		"cluster": map[string]interface{}{
			"name": clusterName,
		},
		"talosControlPlane": map[string]interface{}{
			"name": talosControlPlaneName,
		},
		"random": random,
	}

	tpl, err := template.New("talosControlPlane machine name generator").Option("missingkey=error").Parse(templateString)
	if err != nil {
		return "", errors.Wrapf(err, "parsing template %q", templateString)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", errors.Wrap(err, "rendering template")
	}

	name := buf.String()
	if len(name) > maxNameLength {
		name = name[:maxGeneratedNameLength] + utilrand.String(randomLength)
	}

	return name, nil
}
