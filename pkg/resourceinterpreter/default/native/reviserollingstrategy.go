package native

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/rollout"
)

type reviseRollingStrategyInterpreter func(object *unstructured.Unstructured, rollingStrategy *configv1alpha1.RollingStrategy) (*unstructured.Unstructured, error)

func reviseAllDefaultRollingStrategyInterpreter() map[schema.GroupVersionKind]reviseRollingStrategyInterpreter {
	s := make(map[schema.GroupVersionKind]reviseRollingStrategyInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetReviseRollingStrategy
	return s
}

func cloneSetReviseRollingStrategy(object *unstructured.Unstructured, rollingStrategy *configv1alpha1.RollingStrategy) (*unstructured.Unstructured, error) {
	var err error

	if rollingStrategy.Partition == nil {
		err = unstructured.SetNestedField(object.Object, int64(0), "spec", "updateStrategy", "partition")
	} else if rollingStrategy.Partition.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.Partition.IntVal), "spec", "updateStrategy", "partition")
	} else if rollingStrategy.Partition.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.Partition.StrVal, "spec", "updateStrategy", "partition")
	}
	if err != nil {
		klog.Errorf("Failed to set object(%s).spec.updateStrategy.partition field, err %v", object.GroupVersionKind().String(), err)
		return nil, err
	}

	if rollingStrategy.MaxUnavailable == nil {
		unstructured.RemoveNestedField(object.Object, "spec", "updateStrategy", "maxUnavailable")
	} else if rollingStrategy.MaxUnavailable.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.MaxUnavailable.IntVal), "spec", "updateStrategy", "maxUnavailable")
	} else if rollingStrategy.MaxUnavailable.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.MaxUnavailable.StrVal, "spec", "updateStrategy", "maxUnavailable")
	}
	if err != nil {
		klog.Errorf("Failed to set object(%s).spec.updateStrategy.maxUnavailable field, err %v", object.GroupVersionKind().String(), err)
		return nil, err
	}

	if rollingStrategy.MaxSurge == nil {
		unstructured.RemoveNestedField(object.Object, "spec", "updateStrategy", "maxSurge")
	} else if rollingStrategy.MaxSurge.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.MaxSurge.IntVal), "spec", "updateStrategy", "maxSurge")
	} else if rollingStrategy.MaxSurge.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.MaxSurge.StrVal, "spec", "updateStrategy", "maxSurge")
	}
	if err != nil {
		klog.Errorf("Failed to set object(%s).spec.updateStrategy.maxSurge field, err %v", object.GroupVersionKind().String(), err)
		return nil, err
	}

	return object, nil
}
