/*
Copyright 2021 The Karmada Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package binding

import (
	"context"
	"strconv"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/features"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter"
	"github.com/karmada-io/karmada/pkg/rollout"
	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/helper"
	"github.com/karmada-io/karmada/pkg/util/names"
	"github.com/karmada-io/karmada/pkg/util/overridemanager"
)

// ensureWork ensure Work to be created or updated.
func ensureWork(
	ctx context.Context, c client.Client, interpreter resourceinterpreter.ResourceInterpreter, resourceTemplate *unstructured.Unstructured,
	overrideManager overridemanager.OverrideManager, binding metav1.Object, scope apiextensionsv1.ResourceScope,
) error {
	var targetClusters []workv1alpha2.TargetCluster
	var placement *policyv1alpha1.Placement
	var requiredByBindingSnapshot []workv1alpha2.BindingSnapshot
	var replicas int32
	var conflictResolutionInBinding policyv1alpha1.ConflictResolution
	var suspension *policyv1alpha1.Suspension
	switch scope {
	case apiextensionsv1.NamespaceScoped:
		bindingObj := binding.(*workv1alpha2.ResourceBinding)
		targetClusters = bindingObj.Spec.Clusters
		requiredByBindingSnapshot = bindingObj.Spec.RequiredBy
		placement = bindingObj.Spec.Placement
		replicas = bindingObj.Spec.Replicas
		conflictResolutionInBinding = bindingObj.Spec.ConflictResolution
		suspension = bindingObj.Spec.Suspension
	case apiextensionsv1.ClusterScoped:
		bindingObj := binding.(*workv1alpha2.ClusterResourceBinding)
		targetClusters = bindingObj.Spec.Clusters
		requiredByBindingSnapshot = bindingObj.Spec.RequiredBy
		placement = bindingObj.Spec.Placement
		replicas = bindingObj.Spec.Replicas
		conflictResolutionInBinding = bindingObj.Spec.ConflictResolution
		suspension = bindingObj.Spec.Suspension
	}

	targetClusters = mergeTargetClusters(targetClusters, requiredByBindingSnapshot)

	var jobCompletions []workv1alpha2.TargetCluster
	var err error
	if resourceTemplate.GetKind() == util.JobKind {
		jobCompletions, err = divideReplicasByJobCompletions(resourceTemplate, targetClusters)
		if err != nil {
			return err
		}
	}

	resourceTemplate, err = setTemplateHashAnnotationIfNeeded(interpreter, binding, resourceTemplate)
	if err != nil {
		klog.Errorf("Failed to set template hash annotation on the resourceTemplate(%s/%s/%s): %v",
			resourceTemplate.GetKind(), resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
		return err
	}

	workloads := make(map[string]*unstructured.Unstructured, len(targetClusters))
	for i := range targetClusters {
		targetCluster := targetClusters[i]
		clonedWorkload := resourceTemplate.DeepCopy()

		// If and only if the resource template has replicas, and the replica scheduling policy is divided,
		// we need to revise replicas.
		if needReviseReplicas(replicas, placement) {
			if interpreter.HookEnabled(clonedWorkload.GroupVersionKind(), configv1alpha1.InterpreterOperationReviseReplica) {
				clonedWorkload, err = interpreter.ReviseReplica(clonedWorkload, int64(targetCluster.Replicas))
				if err != nil {
					klog.Errorf("Failed to revise replica for %s/%s/%s in cluster %s, err is: %v",
						resourceTemplate.GetKind(), resourceTemplate.GetNamespace(), resourceTemplate.GetName(), targetCluster.Name, err)
					return err
				}
			}

			// Set allocated completions for Job only when the '.spec.completions' field not omitted from resource template.
			// For jobs running with a 'work queue' usually leaves '.spec.completions' unset, in that case we skip
			// setting this field as well.
			// Refer to: https://kubernetes.io/docs/concepts/workloads/controllers/job/#parallel-jobs.
			if len(jobCompletions) > 0 {
				if err = helper.ApplyReplica(clonedWorkload, int64(jobCompletions[i].Replicas), util.CompletionsField); err != nil {
					klog.Errorf("Failed to apply Completions for %s/%s/%s in cluster %s, err is: %v",
						clonedWorkload.GetKind(), clonedWorkload.GetNamespace(), clonedWorkload.GetName(), targetCluster.Name, err)
					return err
				}
			}
		}
		workloads[targetCluster.Name] = clonedWorkload
	}

	workloads, err = alignWithRolloutStrategy(ctx, c, binding, interpreter, resourceTemplate, workloads, scope, targetClusters)
	if err != nil {
		klog.Errorf("Failed to align workloads with rolloutStrategy, err: %v", err)
		return err
	}

	for i := range targetClusters {
		targetCluster := targetClusters[i]
		clonedWorkload := workloads[targetCluster.Name]
		workNamespace := names.GenerateExecutionSpaceName(targetCluster.Name)

		// We should call ApplyOverridePolicies last, as override rules have the highest priority
		cops, ops, err := overrideManager.ApplyOverridePolicies(clonedWorkload, targetCluster.Name)
		if err != nil {
			klog.Errorf("Failed to apply overrides for %s/%s/%s, err is: %v", clonedWorkload.GetKind(), clonedWorkload.GetNamespace(), clonedWorkload.GetName(), err)
			return err
		}
		workLabel := mergeLabel(clonedWorkload, binding, scope)

		annotations := mergeAnnotations(clonedWorkload, binding, scope)
		annotations = mergeConflictResolution(clonedWorkload, conflictResolutionInBinding, annotations)
		annotations, err = RecordAppliedOverrides(cops, ops, annotations)
		if err != nil {
			klog.Errorf("Failed to record appliedOverrides, Error: %v", err)
			return err
		}

		workMeta := metav1.ObjectMeta{
			Name:        names.GenerateWorkName(clonedWorkload.GetKind(), clonedWorkload.GetName(), clonedWorkload.GetNamespace()),
			Namespace:   workNamespace,
			Finalizers:  []string{util.ExecutionControllerFinalizer},
			Labels:      workLabel,
			Annotations: annotations,
		}

		suspendDispatching := shouldSuspendDispatching(suspension, targetCluster)

		if err = helper.CreateOrUpdateWork(ctx, c, workMeta, clonedWorkload, &suspendDispatching); err != nil {
			return err
		}
	}
	return nil
}

func mergeTargetClusters(targetClusters []workv1alpha2.TargetCluster, requiredByBindingSnapshot []workv1alpha2.BindingSnapshot) []workv1alpha2.TargetCluster {
	if len(requiredByBindingSnapshot) == 0 {
		return targetClusters
	}

	scheduledClusterNames := util.ConvertToClusterNames(targetClusters)

	for _, requiredByBinding := range requiredByBindingSnapshot {
		for _, targetCluster := range requiredByBinding.Clusters {
			if !scheduledClusterNames.Has(targetCluster.Name) {
				scheduledClusterNames.Insert(targetCluster.Name)
				targetClusters = append(targetClusters, targetCluster)
			}
		}
	}

	return targetClusters
}

func mergeLabel(workload *unstructured.Unstructured, binding metav1.Object, scope apiextensionsv1.ResourceScope) map[string]string {
	var workLabel = make(map[string]string)
	if scope == apiextensionsv1.NamespaceScoped {
		bindingID := util.GetLabelValue(binding.GetLabels(), workv1alpha2.ResourceBindingPermanentIDLabel)
		util.MergeLabel(workload, workv1alpha2.ResourceBindingPermanentIDLabel, bindingID)
		workLabel[workv1alpha2.ResourceBindingPermanentIDLabel] = bindingID
	} else {
		bindingID := util.GetLabelValue(binding.GetLabels(), workv1alpha2.ClusterResourceBindingPermanentIDLabel)
		util.MergeLabel(workload, workv1alpha2.ClusterResourceBindingPermanentIDLabel, bindingID)
		workLabel[workv1alpha2.ClusterResourceBindingPermanentIDLabel] = bindingID
	}
	return workLabel
}

func mergeAnnotations(workload *unstructured.Unstructured, binding metav1.Object, scope apiextensionsv1.ResourceScope) map[string]string {
	annotations := make(map[string]string)
	if workload.GetGeneration() > 0 {
		util.MergeAnnotation(workload, workv1alpha2.ResourceTemplateGenerationAnnotationKey, strconv.FormatInt(workload.GetGeneration(), 10))
	}

	if scope == apiextensionsv1.NamespaceScoped {
		util.MergeAnnotation(workload, workv1alpha2.ResourceBindingNamespaceAnnotationKey, binding.GetNamespace())
		util.MergeAnnotation(workload, workv1alpha2.ResourceBindingNameAnnotationKey, binding.GetName())
		annotations[workv1alpha2.ResourceBindingNamespaceAnnotationKey] = binding.GetNamespace()
		annotations[workv1alpha2.ResourceBindingNameAnnotationKey] = binding.GetName()
	} else {
		util.MergeAnnotation(workload, workv1alpha2.ClusterResourceBindingAnnotationKey, binding.GetName())
		annotations[workv1alpha2.ClusterResourceBindingAnnotationKey] = binding.GetName()
	}

	return annotations
}

// RecordAppliedOverrides record applied (cluster) overrides to annotations
func RecordAppliedOverrides(cops *overridemanager.AppliedOverrides, ops *overridemanager.AppliedOverrides,
	annotations map[string]string) (map[string]string, error) {
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if cops != nil {
		appliedBytes, err := cops.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if appliedBytes != nil {
			annotations[util.AppliedClusterOverrides] = string(appliedBytes)
		}
	}

	if ops != nil {
		appliedBytes, err := ops.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if appliedBytes != nil {
			annotations[util.AppliedOverrides] = string(appliedBytes)
		}
	}

	return annotations, nil
}

// mergeConflictResolution determine the conflictResolution annotation of Work: preferentially inherit from RT, then RB
func mergeConflictResolution(workload *unstructured.Unstructured, conflictResolutionInBinding policyv1alpha1.ConflictResolution,
	annotations map[string]string) map[string]string {
	// conflictResolutionInRT refer to the annotation in ResourceTemplate
	conflictResolutionInRT := util.GetAnnotationValue(workload.GetAnnotations(), workv1alpha2.ResourceConflictResolutionAnnotation)

	// the final conflictResolution annotation value of Work inherit from RT preferentially
	// so if conflictResolution annotation is defined in RT already, just copy the value and return
	if conflictResolutionInRT == workv1alpha2.ResourceConflictResolutionOverwrite || conflictResolutionInRT == workv1alpha2.ResourceConflictResolutionAbort {
		annotations[workv1alpha2.ResourceConflictResolutionAnnotation] = conflictResolutionInRT
		return annotations
	} else if conflictResolutionInRT != "" {
		// ignore its value and add logs if conflictResolutionInRT is neither abort nor overwrite.
		klog.Warningf("Ignore the invalid conflict-resolution annotation in ResourceTemplate %s/%s/%s: %s",
			workload.GetKind(), workload.GetNamespace(), workload.GetName(), conflictResolutionInRT)
	}

	if conflictResolutionInBinding == policyv1alpha1.ConflictOverwrite {
		annotations[workv1alpha2.ResourceConflictResolutionAnnotation] = workv1alpha2.ResourceConflictResolutionOverwrite
		return annotations
	}

	annotations[workv1alpha2.ResourceConflictResolutionAnnotation] = workv1alpha2.ResourceConflictResolutionAbort
	return annotations
}

func divideReplicasByJobCompletions(workload *unstructured.Unstructured, clusters []workv1alpha2.TargetCluster) ([]workv1alpha2.TargetCluster, error) {
	var targetClusters []workv1alpha2.TargetCluster
	completions, found, err := unstructured.NestedInt64(workload.Object, util.SpecField, util.CompletionsField)
	if err != nil {
		return nil, err
	}

	if found {
		targetClusters = helper.SpreadReplicasByTargetClusters(int32(completions), clusters, nil)
	}

	return targetClusters, nil
}

func needReviseReplicas(replicas int32, placement *policyv1alpha1.Placement) bool {
	return replicas > 0 && placement != nil && placement.ReplicaSchedulingType() == policyv1alpha1.ReplicaSchedulingTypeDivided
}

func shouldSuspendDispatching(suspension *policyv1alpha1.Suspension, targetCluster workv1alpha2.TargetCluster) bool {
	if suspension == nil {
		return false
	}

	suspendDispatching := ptr.Deref(suspension.Dispatching, false)

	if !suspendDispatching && suspension.DispatchingOnClusters != nil {
		for _, cluster := range suspension.DispatchingOnClusters.ClusterNames {
			if cluster == targetCluster.Name {
				suspendDispatching = true
				break
			}
		}
	}
	return suspendDispatching
}

func careAboutRollingStrategy(interpreter resourceinterpreter.ResourceInterpreter, objGVK schema.GroupVersionKind) bool {
	return features.FeatureGate.Enabled(features.AlignRollingStrategy) &&
		interpreter.HookEnabled(objGVK, configv1alpha1.InterpreterOperationRollingStrategy) &&
		interpreter.HookEnabled(objGVK, configv1alpha1.InterpreterOperationReviseRollingStrategy) &&
		interpreter.HookEnabled(objGVK, configv1alpha1.InterpreterOperationRollingStatus)
}

func setTemplateHashAnnotationIfNeeded(interpreter resourceinterpreter.ResourceInterpreter, binding metav1.Object, resourceTemplate *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if !careAboutRollingStrategy(interpreter, resourceTemplate.GroupVersionKind()) {
		return resourceTemplate, nil
	}

	// we hope sync resourceTemplate when the resourceTemplate has been scheduled. 不会出现这种情况吧？未被调度的修改，不会进入当前函数
	if err := isFullyScheduled(binding, resourceTemplate); err != nil {
		return nil, err
	}
	hash, err := calculateTemplateHash(resourceTemplate)
	if err != nil {
		return nil, err
	}
	// must deepcopy before write it.
	resourceTemplate = resourceTemplate.DeepCopy()
	replaceAnnotation(resourceTemplate, workv1alpha2.ResourceTemplateTemplateHashAnnotationKey, hash)
	return resourceTemplate, nil
}

func alignWithRolloutStrategy(
	ctx context.Context,
	c client.Client,
	binding metav1.Object,
	interpreter resourceinterpreter.ResourceInterpreter,
	resourceTemplate *unstructured.Unstructured,
	workloads map[string]*unstructured.Unstructured,
	scope apiextensionsv1.ResourceScope,
	targetClusters []workv1alpha2.TargetCluster,
) (map[string]*unstructured.Unstructured, error) {
	if !careAboutRollingStrategy(interpreter, resourceTemplate.GroupVersionKind()) {
		return workloads, nil
	}

	var bindingID string
	if scope == apiextensionsv1.NamespaceScoped {
		bindingID = binding.GetLabels()[workv1alpha2.ResourceBindingPermanentIDLabel]
	} else {
		bindingID = binding.GetLabels()[workv1alpha2.ClusterResourceBindingPermanentIDLabel]
	}
	works, err := helper.GetActiveWorksByBindingID(ctx, c, bindingID, scope == apiextensionsv1.NamespaceScoped)
	if err != nil {
		return nil, err
	}

	// Ensure the behavior of rolling strategy (e.g., maxUnavailable, maxSurge, partition) in federation is consistent with its semantics.
	workloads, err = rollout.NewAdapterRolloutInterpreter(interpreter).AlignRollingStrategy(resourceTemplate, workloads, targetClusters, works)
	if err != nil {
		klog.Errorf("Failed to align rolling strategy for %s/%s/%s, err is: %v", resourceTemplate.GetKind(), resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
		return nil, err
	}
	return workloads, nil
}
