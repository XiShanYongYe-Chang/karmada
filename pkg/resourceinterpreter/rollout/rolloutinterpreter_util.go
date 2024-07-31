package rollout

import (
	"fmt"
	"github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util"
)

// rollingContext is the context for rolling update when aligning rolling strategy.
type rollingContext struct {
	// ClusterName of this rolling context
	ClusterName string

	// DesiredReplicas of the workload
	DesiredReplicas int32

	// CurrentReplicas in current applied work's manifest.
	CurrentReplicas int32

	// SpecRevision is the pod template hash in current applied work's manifest.
	SpecRevision string

	// RollingStrategy of the workload
	RollingStrategy *configv1alpha1.RollingStrategy

	// Status of the workload
	Status *configv1alpha1.UnifiedRollingStatus
}

func checkRollingStrategyAligned(federation *rollingContext, members []*rollingContext) error {
	var (
		memberMaxSurge       int32 = 0
		memberPartition      int32 = 0
		memberMaxUnavailable int32 = 0
	)
	for _, member := range members {
		memberMaxSurge += member.RollingStrategy.MaxSurge.IntVal
		memberPartition += member.RollingStrategy.Partition.IntVal
		memberMaxUnavailable += member.RollingStrategy.MaxUnavailable.IntVal
	}
	if federation.RollingStrategy.MaxSurge.IntVal != memberMaxSurge ||
		federation.RollingStrategy.Partition.IntVal != memberPartition ||
		federation.RollingStrategy.MaxUnavailable.IntVal != memberMaxUnavailable {
		return fmt.Errorf("expect max-surge: %d, max-unavailable: %d, partition: %d, "+
			"but got max-surge: %d, max-unavailable: %d, partition: %d",
			federation.RollingStrategy.MaxSurge.IntVal,
			federation.RollingStrategy.MaxUnavailable.IntVal,
			federation.RollingStrategy.Partition.IntVal,
			memberMaxSurge, memberMaxUnavailable, memberPartition)
	}
	return nil
}

// normalizeRollingStatus return a normalized status for given status
func normalizeRollingStatus(status *configv1alpha1.UnifiedRollingStatus) *configv1alpha1.UnifiedRollingStatus {
	if status == nil {
		status = &configv1alpha1.UnifiedRollingStatus{}
	}
	if status.Replicas == nil {
		status.Replicas = pointer.Int32(0)
	}
	if status.ReadyReplicas == nil {
		status.ReadyReplicas = pointer.Int32(0)
	}
	if status.AvailableReplicas == nil {
		status.AvailableReplicas = pointer.Int32(0)
	}
	if status.UpdatedReplicas == nil {
		status.UpdatedReplicas = pointer.Int32(0)
	}
	return status
}

// normalizeRollingStrategy return a normalized rolling strategy for given strategy
func normalizeRollingStrategy(strategy *configv1alpha1.RollingStrategy, replicas int32) (*configv1alpha1.RollingStrategy, error) {
	var err error
	if strategy == nil {
		strategy = &configv1alpha1.RollingStrategy{}
	}

	if strategy.Partition == nil {
		strategy.Partition = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	} else {
		strategy.Partition, err = transIntValueIfNeeds(replicas, strategy.Partition)
		if err != nil {
			return nil, fmt.Errorf("failed to translate partition value: %v", err)
		}
	}

	if strategy.MaxUnavailable == nil {
		strategy.MaxUnavailable = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	} else {
		strategy.MaxUnavailable, err = transIntValueIfNeeds(replicas, strategy.MaxUnavailable)
		if err != nil {
			return nil, fmt.Errorf("failed to translate max-unavailable value: %v", err)
		}
	}

	if strategy.MaxSurge == nil {
		strategy.MaxSurge = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	} else {
		strategy.MaxSurge, err = transIntValueIfNeeds(replicas, strategy.MaxSurge)
		if err != nil {
			return nil, fmt.Errorf("failed to translate max-surge value: %v", err)
		}
	}
	return strategy, nil
}

// isMemberGenerationConsistent checks if member generation is consistent with work's manifests
func isMemberGenerationConsistent(member *unstructured.Unstructured, status *configv1alpha1.UnifiedRollingStatus) bool {
	// check if member controller is still reconciling
	if status.Generation != nil && status.ObservedGeneration != nil && *status.Generation != *status.ObservedGeneration {
		return false
	}
	// check if member status is inconsistent with corresponding work's manifest
	resourceTemplateGenerationTarget := util.GetAnnotationValue(member.GetAnnotations(), v1alpha2.ResourceTemplateGenerationAnnotationKey)
	if status.ResourceTemplateGeneration != nil && *status.ResourceTemplateGeneration > 0 &&
		strconv.Itoa(int(*status.ResourceTemplateGeneration)) != resourceTemplateGenerationTarget {
		return false
	}
	return true
}

// normalizeRollingContext normalizes the rolling context for each member
func normalizeRollingContext(member, federation *rollingContext, desired int32) error {
	var err error
	member.DesiredReplicas = desired
	member.Status = normalizeRollingStatus(member.Status)
	member.RollingStrategy, err = normalizeRollingStrategy(member.RollingStrategy, federation.DesiredReplicas)
	if err != nil {
		return err
	}

	// if current member revision is inconsistent with federation,
	// consider all its replicas is old revision.
	if member.SpecRevision != federation.SpecRevision {
		member.Status.UpdatedReplicas = pointer.Int32(0)
		member.Status.UpdatedReadyReplicas = pointer.Int32(0)
	}
	return nil
}

// transIntValueIfNeeds translates the int or percent value to int value if needed.
func transIntValueIfNeeds(replicas int32, partition *intstr.IntOrString) (*intstr.IntOrString, error) {
	if partition.Type == intstr.Int {
		return partition, nil
	}
	intValue, err := intstr.GetScaledValueFromIntOrPercent(partition, int(replicas), true)
	if err != nil {
		return nil, err
	}
	return &intstr.IntOrString{Type: intstr.Int, IntVal: int32(intValue)}, nil
}
