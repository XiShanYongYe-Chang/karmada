package rollout

import (
	"fmt"
	"strconv"

	"k8s.io/utils/integer"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
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

// OldRevisionReplicas return the old revision replicas.
func (c *rollingContext) OldRevisionReplicas() int32 {
	return *c.Status.Replicas - *c.Status.UpdatedReplicas
}

// UpdatedUnavailableReplicas return the updated unavailable replicas.
func (c *rollingContext) UpdatedUnavailableReplicas() int32 {
	updatedReadyReplicas := int32(0)
	if c.Status.UpdatedReadyReplicas != nil {
		updatedReadyReplicas = *c.Status.UpdatedReadyReplicas
	}
	return integer.Int32Min(*c.Status.UpdatedReplicas-updatedReadyReplicas, 0)
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

	strategy.Partition, err = getScaledIntOrStringFromIntOrPercent(strategy.Partition, replicas)
	if err != nil {
		return nil, fmt.Errorf("failed to get scaled partition value: %v", err)
	}
	strategy.MaxUnavailable, err = getScaledIntOrStringFromIntOrPercent(strategy.MaxUnavailable, replicas)
	if err != nil {
		return nil, fmt.Errorf("failed to get scaled maxUnavailable value: %v", err)
	}
	strategy.MaxSurge, err = getScaledIntOrStringFromIntOrPercent(strategy.MaxSurge, replicas)
	if err != nil {
		return nil, fmt.Errorf("failed to get scaled maxSurge value: %v", err)
	}
	return strategy, nil
}

func getScaledIntOrStringFromIntOrPercent(intOrPercent *intstr.IntOrString, total int32) (*intstr.IntOrString, error) {
	if intOrPercent == nil {
		return &intstr.IntOrString{Type: intstr.Int, IntVal: 0}, nil
	}

	// short path
	if intOrPercent.Type == intstr.Int {
		return intOrPercent, nil
	}

	intValue, err := intstr.GetScaledValueFromIntOrPercent(intOrPercent, int(total), true)
	if err != nil {
		return nil, err
	}
	return &intstr.IntOrString{Type: intstr.Int, IntVal: int32(intValue)}, nil
}

// isWorkloadGenerationConsistent checks if member generation is consistent with work's manifests
func isWorkloadGenerationConsistent(workload *unstructured.Unstructured, status *configv1alpha1.UnifiedRollingStatus) bool {
	// check if workload controller is still reconciling
	if status.Generation != nil && status.ObservedGeneration != nil &&
		*status.Generation != *status.ObservedGeneration {
		return false
	}
	// check if workload status is inconsistent with corresponding work's manifest
	resourceTemplateGeneration := util.GetAnnotationValue(workload.GetAnnotations(), v1alpha2.ResourceTemplateGenerationAnnotationKey)
	if status.ResourceTemplateGeneration != nil && *status.ResourceTemplateGeneration > 0 &&
		strconv.Itoa(int(*status.ResourceTemplateGeneration)) != resourceTemplateGeneration {
		return false
	}
	return true
}

// normalizeRollingContext normalizes the rolling context for each member
func normalizeRollingContext(member, federation *rollingContext, desired int32) (*rollingContext, error) {
	var err error
	member.DesiredReplicas = desired
	member.Status = normalizeRollingStatus(member.Status)
	// 这里用 federation.DesiredReplicas，如果是百分比的话，会计算出错的吧，因为对于每个集群上的workload来说，总值是之前的副本数
	// 而且之前的结果也是我们设置上的，应该用的都是数字，即使真的有百分比存在，这里副本数使用desired也会更好。
	member.RollingStrategy, err = normalizeRollingStrategy(member.RollingStrategy, federation.DesiredReplicas)
	if err != nil {
		return nil, err
	}

	// if current member revision is inconsistent with federation,
	// consider all its replicas is old revision.
	if member.SpecRevision != federation.SpecRevision {
		member.Status.UpdatedReplicas = pointer.Int32(0)
		member.Status.UpdatedReadyReplicas = pointer.Int32(0)
	}
	return member, nil
}
