module github.com/talos-systems/cluster-api-control-plane-provider-talos

go 1.16

replace (
	// keep older versions of k8s.io packages to keep compatiblity with cluster-api
	k8s.io/api v0.21.3 => k8s.io/api v0.20.5
	k8s.io/api-server v0.21.3 => k8s.io/api-server v0.20.5
	k8s.io/apimachinery v0.21.3 => k8s.io/apimachinery v0.20.5
	k8s.io/client-go v0.21.3 => k8s.io/client-go v0.20.5

	sigs.k8s.io/cluster-api v0.3.20 => sigs.k8s.io/cluster-api v0.3.9
)

require (
	github.com/go-logr/logr v0.4.0
	github.com/go-logr/zapr v0.2.0 // indirect
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.14.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	github.com/talos-systems/capi-utils v0.0.0-20210906195159-c20b1a80b427
	github.com/talos-systems/cluster-api-bootstrap-provider-talos v0.2.0
	github.com/talos-systems/talos/pkg/machinery v0.12.0
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/apiserver v0.21.3
	k8s.io/client-go v0.21.3
	k8s.io/utils v0.0.0-20210722164352-7f3ee0f31471
	sigs.k8s.io/cluster-api v0.3.20
	sigs.k8s.io/controller-runtime v0.6.3
)
