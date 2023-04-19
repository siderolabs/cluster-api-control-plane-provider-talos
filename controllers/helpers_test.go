// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gobuffalo/flect"
	"github.com/pkg/errors"
	"github.com/siderolabs/crypto/tls"
	"github.com/siderolabs/crypto/x509"
	"github.com/siderolabs/talos/pkg/grpc/gen"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/api/storage"
	"github.com/siderolabs/talos/pkg/machinery/config/types/v1alpha1/generate"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/typ.v4/slices"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/controllers"
)

var fakeScheme = runtime.NewScheme()

// test utils.

type reconcilerOptions struct {
	cluster *types.NamespacedName
}

type reconcilerOption func(opts *reconcilerOptions)

func withCluster(key types.NamespacedName) reconcilerOption {
	return func(o *reconcilerOptions) {
		o.cluster = &key
	}
}

func newReconciler(client client.Client, opts ...reconcilerOption) *controllers.TalosControlPlaneReconciler {
	logger := zap.New(zap.WriteTo(os.Stdout))
	logf.SetLogger(logger)

	var options reconcilerOptions

	for _, opt := range opts {
		opt(&options)
	}

	var tracker *remote.ClusterCacheTracker
	if options.cluster != nil {
		tracker = remote.NewTestClusterCacheTracker(logger, client, fakeScheme, *options.cluster)
	}

	return &controllers.TalosControlPlaneReconciler{
		Client:    client,
		Log:       logger,
		APIReader: client,
		Tracker:   tracker,
	}
}

func newFakeClient(initObjs ...client.Object) client.Client {
	return &fakeClient{
		startTime: time.Now(),
		Client:    fake.NewClientBuilder().WithObjects(initObjs...).WithScheme(fakeScheme).Build(),
	}
}

type fakeClient struct {
	startTime time.Time
	mux       sync.Mutex
	client.Client
}

func createSecrets(ctx context.Context, obj client.Client, cluster *clusterv1.Cluster, secretsBundle *generate.SecretsBundle, machineAddress string) error {
	ca := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ca",
			Namespace: "default",
		},
	}

	if err := obj.Create(ctx, ca); err != nil {
		return err
	}

	input, err := generate.NewInput(cluster.Name, "https://localhost:6443", constants.DefaultKubernetesVersion, secretsBundle)
	if err != nil {
		return err
	}

	config, err := generate.Talosconfig(input)
	if err != nil {
		return err
	}

	config.Contexts[config.Context].Endpoints = []string{fmt.Sprintf("https://%s", machineAddress)}

	data, err := config.Bytes()
	if err != nil {
		return err
	}

	talosconfigSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-talosconfig",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			"talosconfig": data,
		},
	}

	return obj.Create(ctx, talosconfigSecret)
}

func createMachineNodePair(name string, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, ready bool, address string) (*clusterv1.Machine, *corev1.Node) {
	machine := &clusterv1.Machine{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Machine",
			APIVersion: clusterv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      name,
			Labels:    ControlPlaneMachineLabelsForCluster(tcp, cluster.Name),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			InfrastructureRef: corev1.ObjectReference{
				Kind:       GenericInfrastructureMachineCRD.Kind,
				APIVersion: GenericInfrastructureMachineCRD.APIVersion,
				Name:       GenericInfrastructureMachineCRD.Name,
				Namespace:  GenericInfrastructureMachineCRD.Namespace,
			},
		},
		Status: clusterv1.MachineStatus{
			NodeRef: &corev1.ObjectReference{
				Kind:       "Node",
				APIVersion: corev1.SchemeGroupVersion.String(),
				Name:       name,
			},
			Addresses: clusterv1.MachineAddresses{
				{
					Type:    clusterv1.MachineInternalIP,
					Address: address,
				},
			},
		},
	}
	machine.Default()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{constants.LabelNodeRoleControlPlane: ""},
		},
	}

	if ready {
		node.Spec.ProviderID = fmt.Sprintf("test://%s", machine.GetName())
		node.Status.Conditions = []corev1.NodeCondition{
			{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			},
		}
	}
	return machine, node
}

// newCluster return a CAPI cluster object.
func newCluster(namespacedName *types.NamespacedName) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespacedName.Namespace,
			Name:      namespacedName.Name,
		},
	}
}

// ControlPlaneMachineLabelsForCluster returns a set of labels to add to a control plane machine for this specific cluster.
func ControlPlaneMachineLabelsForCluster(tcp *controlplanev1.TalosControlPlane, clusterName string) map[string]string {
	labels := map[string]string{}

	// Always force these labels over the ones coming from the spec.
	labels[clusterv1.ClusterNameLabel] = clusterName
	labels[clusterv1.MachineControlPlaneLabel] = ""
	// Note: MustFormatValue is used here as the label value can be a hash if the control plane name is longer than 63 characters.
	labels[clusterv1.MachineControlPlaneNameLabel] = MustFormatValue(tcp.Name)
	return labels
}

// MustFormatValue returns the passed inputLabelValue if it meets the standards for a Kubernetes label value.
// If the name is not a valid label value this function returns a hash which meets the requirements.
func MustFormatValue(str string) string {
	// a valid Kubernetes label value must:
	// - be less than 64 characters long.
	// - be an empty string OR consist of alphanumeric characters, '-', '_' or '.'.
	// - start and end with an alphanumeric character
	if len(validation.IsValidLabelValue(str)) == 0 {
		return str
	}
	hasher := fnv.New32a()
	_, err := hasher.Write([]byte(str))
	if err != nil {
		// At time of writing the implementation of fnv's Write function can never return an error.
		// If this changes in a future go version this function will panic.
		panic(err)
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hasher.Sum(nil))
}

var (
	// InfrastructureGroupVersion is group version used for infrastructure objects.
	InfrastructureGroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1"}

	// GenericInfrastructureMachineKind is the Kind for the GenericInfrastructureMachine.
	GenericInfrastructureMachineKind = "GenericInfrastructureMachine"

	// GenericInfrastructureMachineCRD is a generic infrastructure machine CRD.
	GenericInfrastructureMachineCRD = untypedCRD(InfrastructureGroupVersion.WithKind(GenericInfrastructureMachineKind))
)

func untypedCRD(gvk schema.GroupVersionKind) *apiextensionsv1.CustomResourceDefinition {
	return generateCRD(gvk, map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {
			Type:                   "object",
			XPreserveUnknownFields: pointer.Bool(true),
		},
		"status": {
			Type:                   "object",
			XPreserveUnknownFields: pointer.Bool(true),
		},
	})
}

func generateCRD(gvk schema.GroupVersionKind, properties map[string]apiextensionsv1.JSONSchemaProps) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			Kind:       "CustomResourceDefinition",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", flect.Pluralize(strings.ToLower(gvk.Kind)), gvk.Group),
			Labels: map[string]string{
				clusterv1.GroupVersion.String(): "v1beta1",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gvk.Group,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:   gvk.Kind,
				Plural: flect.Pluralize(strings.ToLower(gvk.Kind)),
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    gvk.Version,
					Served:  true,
					Storage: true,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:       "object",
							Properties: properties,
						},
					},
				},
			},
		},
	}
}

func startMachineServer(ctx context.Context, secretsBundle *generate.SecretsBundle) (*machineService, string, error) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, "", err
	}

	ca := secretsBundle.Certs.OS

	ips := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}

	netIPs := slices.Map(ips, func(ip netip.Addr) net.IP { return ip.AsSlice() })

	var generator tls.Generator

	generator, err = gen.NewLocalGenerator(ca.Key, ca.Crt)
	if err != nil {
		return nil, "", err
	}

	provider, err := tls.NewRenewingCertificateProvider(generator, x509.IPAddresses(netIPs))
	if err != nil {
		return nil, "", err
	}

	caCertPEM, err := provider.GetCA()
	if err != nil {
		return nil, "", err
	}

	tlsConfig, err := tls.New(
		tls.WithClientAuthType(tls.Mutual),
		tls.WithCACertPEM(caCertPEM),
		tls.WithServerCertificateProvider(provider),
	)

	if err != nil {
		return nil, "", err
	}

	machineServer := grpc.NewServer(grpc.Creds(
		credentials.NewTLS(tlsConfig),
	))

	machineService := &machineService{
		resetChan: make(chan struct{}, 10),
	}

	machine.RegisterMachineServiceServer(machineServer, machineService)
	storage.RegisterStorageServiceServer(machineServer, machineService)

	go func() {
		for {
			err = machineServer.Serve(listener)
			if err == nil || errors.Is(err, grpc.ErrServerStopped) {
				break
			}
		}
	}()
	go func() {
		<-ctx.Done()
		machineServer.Stop()
	}()

	return machineService, listener.Addr().String(), nil
}

//nolint:govet
type machineService struct {
	lock sync.Mutex

	machine.UnimplementedMachineServiceServer
	storage.UnimplementedStorageServiceServer

	etcdMembers         *machine.EtcdMemberListResponse
	serviceListResponse *machine.ServiceListResponse
	resetChan           chan struct{}
}

func (ms *machineService) Bootstrap(ctx context.Context, req *machine.BootstrapRequest) (*machine.BootstrapResponse, error) {
	return &machine.BootstrapResponse{}, nil
}

func (ms *machineService) EtcdMemberList(ctx context.Context, req *machine.EtcdMemberListRequest) (*machine.EtcdMemberListResponse, error) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	if ms.etcdMembers == nil {
		return nil, status.Error(codes.Unavailable, "not available")
	}

	return ms.etcdMembers, nil
}

func (ms *machineService) ServiceList(context.Context, *emptypb.Empty) (*machine.ServiceListResponse, error) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	if ms.serviceListResponse == nil {
		return nil, status.Error(codes.Unavailable, "not available")
	}

	return ms.serviceListResponse, nil
}

func (ms *machineService) setEtcdMembersResponse(resp *machine.EtcdMemberListResponse) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	ms.etcdMembers = resp
}

func (ms *machineService) setServiceListResponse(resp *machine.ServiceListResponse) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	ms.serviceListResponse = resp
}

func init() {
	_ = clusterv1.AddToScheme(fakeScheme)
	_ = apiextensionsv1.AddToScheme(fakeScheme)
	_ = controlplanev1.AddToScheme(fakeScheme)
	_ = corev1.AddToScheme(fakeScheme)
	_ = appsv1.AddToScheme(fakeScheme)
}
