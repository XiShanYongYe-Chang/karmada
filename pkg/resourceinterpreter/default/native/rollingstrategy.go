package native

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/rollout"
)

type rollingStrategyInterpreter func(object *unstructured.Unstructured) (*configv1alpha1.RollingStrategy, error)

func getAllDefaultRollingStrategyInterpreter() map[schema.GroupVersionKind]rollingStrategyInterpreter {
	s := make(map[schema.GroupVersionKind]rollingStrategyInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetRollingStrategy
	return s
}

func cloneSetRollingStrategy(object *unstructured.Unstructured) (*configv1alpha1.RollingStrategy, error) {
	var maxSurge *intstr.IntOrString
	var partition *intstr.IntOrString
	var maxUnavailable *intstr.IntOrString

	m, found, err := unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "partition")
	if err == nil && found {
		partition = unmarshalIntStr(m)
	} else {
		partition = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	m, found, err = unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "maxUnavailable")
	if err == nil && found {
		maxUnavailable = unmarshalIntStr(m)
	} else {
		maxUnavailable = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	m, found, err = unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "maxSurge")
	if err == nil && found {
		maxSurge = unmarshalIntStr(m)
	} else {
		maxSurge = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	return &configv1alpha1.RollingStrategy{
		MaxSurge:       maxSurge,
		Partition:      partition,
		MaxUnavailable: maxUnavailable,
	}, nil
}

// unmarshalIntStr return *intstr.IntOrString
func unmarshalIntStr(m interface{}) *intstr.IntOrString {
	field := &intstr.IntOrString{}
	data, _ := json.Marshal(m)
	_ = json.Unmarshal(data, field)
	return field
}
