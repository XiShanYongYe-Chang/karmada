package binding

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/util"
	hash "github.com/karmada-io/karmada/pkg/util/hasher"
)

// replaceAnnotation replaces the annotation of the object with the given key and value.
func replaceAnnotation(obj *unstructured.Unstructured, key, value string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[key] = value
	obj.SetAnnotations(annotations)
}

// isFullyScheduled checks if the workload is fully scheduled.
func isFullyScheduled(binding metav1.Object, workload *unstructured.Unstructured) error {
	resourceBinding, ok := binding.(*workv1alpha2.ResourceBinding)
	if !ok {
		return nil
	}
	if resourceBinding.Spec.Placement == nil ||
		resourceBinding.Spec.Placement.ReplicaScheduling == nil ||
		resourceBinding.Spec.Placement.ReplicaScheduling.ReplicaSchedulingType != policyv1alpha1.ReplicaSchedulingTypeDivided {
		return nil
	}
	if sumReplicas := util.GetSumOfReplicas(resourceBinding.Spec.Clusters); sumReplicas != resourceBinding.Spec.Replicas {
		err := fmt.Errorf("workload %s/%s/%s is not scheduled correctly, desired %d, but got %d",
			workload.GetKind(), workload.GetNamespace(), workload.GetName(), resourceBinding.Spec.Replicas, sumReplicas)
		klog.Error(err.Error())
		return err
	}
	return nil
}

// calculateTemplateHash calculates the hash of the template of the workload.
func calculateTemplateHash(workload *unstructured.Unstructured) (string, error) {
	// TODO: Template should get from resource interpreter method，这又是一个新的扩展点是么，是否可以通过其他方式实现？
	template, ok, err := unstructured.NestedFieldNoCopy(workload.Object, "spec", "template")
	if err != nil || !ok {
		return "", fmt.Errorf("failed to nested unstructured template for %s(%s/%s): %v", workload.GetKind(), workload.GetNamespace(), workload.GetName(), err)
	}
	return hash.ComputeHash(template, nil), nil
}
