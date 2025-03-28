//go:build tools
// +build tools

package openshift

import (
	_ "github.com/mikefarah/yq/v4/cmd"
	_ "github.com/onsi/ginkgo/v2/ginkgo/command"
	_ "github.com/onsi/ginkgo/v2/ginkgo/run"
	_ "gotest.tools/gotestsum"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
	_ "sigs.k8s.io/kustomize/kustomize/v5/commands/edit/listbuiltin"
)
