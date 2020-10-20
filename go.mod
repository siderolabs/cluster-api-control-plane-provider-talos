module github.com/talos-systems/cluster-api-control-plane-provider-talos

go 1.13

require (
	github.com/go-logr/logr v0.1.0
	github.com/golang/protobuf v1.4.2
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/pkg/errors v0.9.1
	github.com/talos-systems/cluster-api-bootstrap-provider-talos v0.2.0-alpha.6
	github.com/talos-systems/talos/pkg/machinery v0.0.0-20201020161939-d2583e228288
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/apiserver v0.18.6
	k8s.io/client-go v0.18.6
	k8s.io/utils v0.0.0-20200619165400-6e3d28b6ed19
	sigs.k8s.io/cluster-api v0.3.9
	sigs.k8s.io/controller-runtime v0.6.3
)
