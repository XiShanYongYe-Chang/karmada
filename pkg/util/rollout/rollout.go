package rollout

import (
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/karmada-io/karmada/pkg/features"
	"github.com/karmada-io/karmada/pkg/util"
)

var (
	CloneSetGroupVersionKind = schema.GroupVersionKind{
		Group:   "apps.kruise.io",
		Version: "v1alpha1",
		Kind:    "CloneSet",
	}

	DeploymentGroupVersionKind = schema.GroupVersionKind{
		Group:   appsv1.SchemeGroupVersion.Group,
		Version: appsv1.SchemeGroupVersion.Version,
		Kind:    util.DeploymentKind,
	}

	statelessRollingGroupKind = []schema.GroupKind{
		CloneSetGroupVersionKind.GroupKind(),
	}
	statefulRollingGroupKind = []schema.GroupKind{}
)

func IsStatelessRollingKind(groupVersionKind schema.GroupVersionKind) bool {
	for _, gk := range statelessRollingGroupKind {
		if groupVersionKind.GroupKind() == gk {
			return true
		}
	}
	return false
}

func IsStatefulRollingKind(groupVersionKind schema.GroupVersionKind) bool {
	for _, gk := range statefulRollingGroupKind {
		if groupVersionKind.GroupKind() == gk {
			return true
		}
	}
	return false
}

// EnableRolloutInterpreter returns true if the rollout interpreter is enabled for the given groupVersionKind.
func EnableRolloutInterpreter(groupVersionKind schema.GroupVersionKind) bool {
	if !features.FeatureGate.Enabled(features.AlignRollingStrategy) {
		return false
	}
	return IsStatelessRollingKind(groupVersionKind) || IsStatefulRollingKind(groupVersionKind)
}
