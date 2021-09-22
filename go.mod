module github.com/talos-systems/cluster-api-control-plane-provider-talos

go 1.16

require (
	github.com/coreos/go-semver v0.3.0
	github.com/go-logr/logr v0.4.0
	github.com/google/uuid v1.1.2
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.14.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	github.com/talos-systems/capi-utils v0.0.0-20210917140904-9587089e8425
	github.com/talos-systems/cluster-api-bootstrap-provider-talos v0.3.0
	github.com/talos-systems/go-retry v0.3.1
	github.com/talos-systems/talos/pkg/machinery v0.12.3-0.20210920195258-7e63e43eb399
	google.golang.org/grpc v1.40.0
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b
	k8s.io/api v0.17.9
	k8s.io/apimachinery v0.17.9
	k8s.io/apiserver v0.17.9
	k8s.io/client-go v0.17.9
	k8s.io/utils v0.0.0-20200619165400-6e3d28b6ed19
	sigs.k8s.io/cluster-api v0.3.23
	sigs.k8s.io/controller-runtime v0.5.14
)
