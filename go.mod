module github.com/talos-systems/cluster-api-control-plane-provider-talos

go 1.13

require (
	cloud.google.com/go v0.47.0 // indirect
	github.com/go-logr/logr v0.1.0
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/pkg/errors v0.9.1
	github.com/talos-systems/cluster-api-bootstrap-provider-talos v0.2.0-alpha.10
	github.com/talos-systems/talos/pkg/machinery v0.0.0-20210218160848-32d25885288f
	k8s.io/api v0.17.9
	k8s.io/apimachinery v0.17.9
	k8s.io/apiserver v0.17.9
	k8s.io/client-go v0.17.9
	k8s.io/utils v0.0.0-20200619165400-6e3d28b6ed19
	sigs.k8s.io/cluster-api v0.3.12
	sigs.k8s.io/controller-runtime v0.5.14
)
