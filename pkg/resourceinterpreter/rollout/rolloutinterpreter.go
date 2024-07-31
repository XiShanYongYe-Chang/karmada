package rollout

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"k8s.io/utils/integer"
	"k8s.io/utils/pointer"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	workv1alpha1 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter"
	"github.com/karmada-io/karmada/pkg/util"
)

// Interpreter is the interface for aligning rolling strateg.
type Interpreter interface {
	AlignRollingStrategy(resourceTemplate *unstructured.Unstructured, workloads map[string]*unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (map[string]*unstructured.Unstructured, error)
}

// statelessRolloutInterpreterImpl is the implementation of Rollout Interpreter for stateless workloads.
type statelessRolloutInterpreterImpl struct {
	resourceinterpreter.ResourceInterpreter
}

// NewAdapterRolloutInterpreter creates a new Rollout Interpreter for given workload type.
func NewAdapterRolloutInterpreter(gvk schema.GroupVersionKind, interpreter resourceinterpreter.ResourceInterpreter) Interpreter {
	// TODO: support stateful rolling interpreter type
	return &statelessRolloutInterpreterImpl{interpreter}
}

// AlignRollingStrategy aligns the rolling strategy for each member based on the given resource template and workloads status.
func (i *statelessRolloutInterpreterImpl) AlignRollingStrategy(resourceTemplate *unstructured.Unstructured, workloads map[string]*unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (map[string]*unstructured.Unstructured, error) {
	strategies, err := i.CalculateRollingStrategy(resourceTemplate, clusters, works)
	if err != nil {
		return nil, err
	}

	revisedWorkloads := make(map[string]*unstructured.Unstructured, len(workloads))
	for cluster, wl := range workloads {
		revisedWorkloads[cluster], err = i.ReviseRollingStrategy(wl, strategies[cluster])
		if err != nil {
			return nil, err
		}
	}
	return revisedWorkloads, nil
}

// CalculateRollingStrategy calculates the rolling strategy for each member based on the given resource template and workloads status.
func (i *statelessRolloutInterpreterImpl) CalculateRollingStrategy(resourceTemplate *unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (map[string]*configv1alpha1.RollingStrategy, error) {
	/*
		@@ Calculate normalized rolling context for federation and members. @@
	*/
	federation, members, err := i.calculateRollingContext(resourceTemplate, clusters, works)
	if err != nil {
		klog.Errorf("Failed to collect context for rolling strategy calculation for %s/%s: %v", resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
		return nil, err
	}

	/*
		@@ Calculate rollingStrategy.Partition for each member. @@
	*/
	func() {
		// updatedReplicasQuota indicate the number of **old-revision** replicas
		// that can be updated in federated level according to `partition`.
		//
		// initialized by `federation.replicas - federation.partition - SUM(member.replicas-member.partition)`
		var updatedReplicasQuota = federation.DesiredReplicas - federation.RollingStrategy.Partition.IntVal
		for _, member := range members {
			// The corresponding work is not applied means all of it revision must be update revision.
			// All replicas of this member will be updated.
			if _, ok := works[member.ClusterName]; !ok {
				updatedReplicasQuota -= member.DesiredReplicas
				continue
			}

			// correct the partition for this member.
			member.RollingStrategy.Partition.IntVal = integer.Int32Min(member.RollingStrategy.Partition.IntVal, *member.Status.Replicas-*member.Status.UpdatedReplicas)

			// We correct the current partition for this member.
			if member.RollingStrategy.Partition != nil && member.RollingStrategy.Partition.IntVal > 0 {
				// maxUpdatedReplicasPossible is the maximum number of replicas that this workload may update.
				// currentPartition should equals to `target.Replicas - maxUpdatedReplicasPossible`, which means
				// we expected the newly-scaled-up replicas is the old revision as much as possible if
				// `target.Replicas > currentReplicas`.
				maxUpdatedReplicasPossible := integer.Int32Max(member.CurrentReplicas-member.RollingStrategy.Partition.IntVal, 0)
				maxUpdatedReplicasPossible = integer.Int32Min(maxUpdatedReplicasPossible, member.DesiredReplicas)
				partitionedReplicasPossible := integer.Int32Max(member.DesiredReplicas, member.CurrentReplicas) - maxUpdatedReplicasPossible
				member.RollingStrategy.Partition = &intstr.IntOrString{Type: intstr.Int, IntVal: partitionedReplicasPossible}
			} else {
				member.RollingStrategy.Partition = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
			}

			if member.SpecRevision == federation.SpecRevision {
				// - For new-revision workload, we should consider it have been updated partially.
				updatedReplicasQuota -= member.DesiredReplicas - int32(member.RollingStrategy.Partition.IntValue())
			} else {
				// - For old-revision workload, we should assume it is fully partitioned.
				member.RollingStrategy.Partition.IntVal = member.DesiredReplicas
			}
		}

		// if updateQuota greater than 0, should distribute the quota to each member cluster.
		if updatedReplicasQuota > 0 {
			// member.partition += MIN(updateQuota, member.current_unavailable)
			//
			// This loop ensure update the old unavailable replicas firstly.
			for _, member := range members {
				updatedReadyReplicas := int32(0)
				if member.Status.UpdatedReadyReplicas != nil {
					updatedReadyReplicas = *member.Status.UpdatedReadyReplicas
				}
				updatedUnavailable := integer.Int32Min(*member.Status.UpdatedReplicas-updatedReadyReplicas, 0)
				currentUnavailable := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas-updatedUnavailable, 0)
				updatedReplicasQuotaToAdd := integer.Int32Min(member.RollingStrategy.Partition.IntVal, updatedReplicasQuota)
				updatedReplicasQuotaToAdd = integer.Int32Min(updatedReplicasQuotaToAdd, currentUnavailable)
				member.RollingStrategy.Partition.IntVal -= updatedReplicasQuotaToAdd
				updatedReplicasQuota -= updatedReplicasQuotaToAdd
			}

			// member.partition += MIN(updateQuota, member.current)
			//
			// This loop ensure sum of member target update replicas equals to federation's.
			for _, member := range members {
				// the number of next updated replicas in this member cluster should
				// not exceed the target updated replicas at federated level.
				updatedReplicasQuotaToAdd := integer.Int32Min(member.RollingStrategy.Partition.IntVal, updatedReplicasQuota)
				member.RollingStrategy.Partition.IntVal -= updatedReplicasQuotaToAdd
				updatedReplicasQuota -= updatedReplicasQuotaToAdd
			}
		}
	}()

	/*
		@@ Calculate rollingStrategy.MaxUnavailable for each member. @@
	*/
	func() {
		// updateTargetQuota indicate the number of **old-revision && ready** replicas
		// that can be updated in federated level according to `maxUnavailable`.
		//
		// initialized by federation.maxUnavailable - SUM(member.unavailable)
		var unavailableQuota = federation.RollingStrategy.MaxUnavailable.IntVal
		for _, member := range members {
			unavailableReplicas := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas, 0)
			unavailableQuotaToAdd := integer.Int32Min(unavailableReplicas, unavailableQuota)
			member.RollingStrategy.MaxUnavailable.IntVal = unavailableQuotaToAdd
			unavailableQuota -= unavailableQuotaToAdd
		}

		// id unavailableQuota greater than 0, should distribute the quota to each member cluster.
		if unavailableQuota > 0 {
			// member.maxUnavailable += MIN(unavailableQuota, member.current_available_to_update)
			//
			// This loop ensure not exceed federation.maxUnavailable by controlling the number of available replicas to update.
			for _, member := range members {
				unavailableReplicas := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas, 0)
				maxUpdatedReplicasPossible := integer.Int32Max(member.DesiredReplicas-member.RollingStrategy.Partition.IntVal, 0)
				currentReplicasToUpdate := integer.Int32Max(maxUpdatedReplicasPossible-*member.Status.UpdatedReplicas, 0)

				var unavailableQuotaToAdd int32
				if member.Status.UpdatedReadyReplicas != nil {
					// more accurate calculation for such workload reporting its updated ready replicas.
					updatedUnavailableReplicas := integer.Int32Max(*member.Status.UpdatedReplicas-*member.Status.UpdatedReadyReplicas, 0)
					currentUnavailableToUpdate := integer.Int32Max(unavailableReplicas-updatedUnavailableReplicas, 0)
					currentAvailableToUpdate := integer.Int32Max(currentReplicasToUpdate-currentUnavailableToUpdate, 0)
					unavailableQuotaToAdd = integer.Int32Min(currentAvailableToUpdate, unavailableQuota)
				} else {
					maxAvailableReplicas := integer.Int32Max(member.DesiredReplicas-unavailableReplicas, 0)
					unavailableQuotaToAdd = integer.Int32Min(currentReplicasToUpdate, unavailableQuota)
					unavailableQuotaToAdd = integer.Int32Min(unavailableQuotaToAdd, maxAvailableReplicas)
				}
				member.RollingStrategy.MaxUnavailable.IntVal += unavailableQuotaToAdd
				unavailableQuota -= unavailableQuotaToAdd
			}

			// member.maxUnavailable += FLOOR(unavailableQuota * member.replicas / federation.replicas)
			//
			// The rest unavailableQuota will not determinate the number of updated replicas, so
			// we just distribute the rest unavailableQuota to each member cluster by replicas weight
			// to ensure the sum of maxUnavailable is equal to federation.maxUnavailable as much as possible.
			totalRestUnavailableQuota := unavailableQuota
			for _, member := range members {
				unavailableQuotaToAdd := int32(math.Floor(float64(totalRestUnavailableQuota*member.DesiredReplicas) / float64(federation.DesiredReplicas)))
				unavailableQuotaToAdd = integer.Int32Min(unavailableQuotaToAdd, unavailableQuota)
				member.RollingStrategy.MaxUnavailable.IntVal += unavailableQuotaToAdd
				unavailableQuota -= unavailableQuotaToAdd
			}

			// handle rest unavailableQuota cased by round down, will not execute
			// more than len(members) times in the following loops.
			currentIndex, totalIndex := 0, len(members)
			for totalIndex > 0 && unavailableQuota > 0 {
				members[currentIndex].RollingStrategy.MaxUnavailable.IntVal += 1
				currentIndex = (currentIndex + 1) % totalIndex
				unavailableQuota--
			}
		}
	}()

	/*
		@@ Calculate rollingStrategy.MaxSurge for each member. @@
	*/
	func() {
		// updateTargetQuota indicate the number of replicas
		// that can be surged in federated level according to `maxSurge`.
		//
		// initialized by federation.maxSurge
		var surgeQuota = federation.RollingStrategy.MaxSurge.IntVal

		// member.maxSurge = MIN(surgeQuota, member.current_to_update)
		//
		// This loop ensure not exceed federation.maxSurge by controlling the number of replicas to surge,
		// and ensure the surge replicas is effective as much as possible.
		for _, member := range members {
			currentRevisionReplicas := integer.Int32Max(*member.Status.Replicas-*member.Status.UpdatedReplicas, 0)
			currentReplicasToUpdate := integer.Int32Max(currentRevisionReplicas-member.RollingStrategy.Partition.IntVal, 0)
			surgeQuotaToAdd := integer.Int32Min(currentReplicasToUpdate, surgeQuota)
			member.RollingStrategy.MaxSurge.IntVal = surgeQuotaToAdd
			surgeQuota -= surgeQuotaToAdd
		}
	}()

	// check the rolling strategy is aligned or not.
	if err = checkRollingStrategyAligned(federation, members); err != nil {
		// just warning it, do not return this error.
		klog.Warningf("Rolling strategy is not aligned for %s/%s: %v", resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
	}

	// correct the invalid rolling strategy.
	for _, member := range members {
		// Most stateless workloads(e.g., Deployment/CloneSet) require that maxUnavailable and maxSurge
		// must not be both 0, otherwise validation admission webhook will deny the create/update request.
		if member.RollingStrategy.MaxSurge.IntVal == 0 && member.RollingStrategy.MaxUnavailable.IntVal == 0 {
			// Hack: We set maxSurge=1 to ensure the update can be allowed by the validation admission,
			// and ensure NO ONE replicas will be updated due to `member.partition = member.replicas`
			// or `member.paused = true`.
			member.RollingStrategy.MaxSurge.IntVal = 1
			member.RollingStrategy.Partition.IntVal = member.DesiredReplicas
			member.RollingStrategy.Paused = pointer.Bool(true)
		}
	}

	strategies := make(map[string]*configv1alpha1.RollingStrategy, len(members))
	for _, member := range members {
		strategies[member.ClusterName] = member.RollingStrategy
	}

	strategyInfo, _ := json.Marshal(strategies)
	klog.V(4).Infof("Calculated rolling strategies for %s/%s in members: %v",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), string(strategyInfo))
	return strategies, nil
}

// calculateRollingContext calculates the rolling context for federation and members.
func (i *statelessRolloutInterpreterImpl) calculateRollingContext(resourceTemplate *unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (*rollingContext, []*rollingContext, error) {
	replicas, _, err := i.GetReplicas(resourceTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get replicas: %v", err)
	}

	strategy, err := i.GetRollingStrategy(resourceTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get rolling strategy: %v", err)
	}

	strategy, err = normalizeRollingStrategy(strategy, replicas)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to normalize rolling strategy: %v", err)
	}

	// MaxUnavailable and MaxSurge should not be both 0,
	// we should ensure at least one of replicas can be updated.
	if replicas > 0 && strategy.MaxUnavailable.IntVal == 0 && strategy.MaxSurge.IntVal == 0 {
		strategy.MaxUnavailable.IntVal = 1
	}

	updateRevision := util.GetAnnotationValue(resourceTemplate.GetAnnotations(), workv1alpha2.ResourceTemplateTemplateHashAnnotationKey)
	federation := &rollingContext{
		DesiredReplicas: replicas,
		RollingStrategy: strategy,
		SpecRevision:    updateRevision,
	}

	clusterMap := make(map[string]int32, len(clusters))
	for _, cluster := range clusters {
		clusterMap[cluster.Name] = cluster.Replicas
	}

	memberMap := make(map[string]*rollingContext, len(clusters))
	for cluster, work := range works {
		if _, ok := clusterMap[cluster]; !ok {
			continue
		}
		memberMap[cluster] = &rollingContext{ClusterName: cluster}

		member := unstructured.Unstructured{}
		if err = json.Unmarshal(work.Spec.Workload.Manifests[0].Raw, &member); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal workload from work: %v", err)
		}

		status := &configv1alpha1.UnifiedRollingStatus{}
		if len(work.Status.ManifestStatuses) > 0 {
			status, err = i.InterpretRollingStatus(&member, work.Status.ManifestStatuses[0].Status)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to interpret rolling status: %v", err)
			}
		}
		status = normalizeRollingStatus(status)
		memberMap[cluster].Status = status

		if !isMemberGenerationConsistent(&member, status) {
			return nil, nil, fmt.Errorf("current work mainifest has not been reconciled in cluster %s", cluster)
		}

		current, _, err := i.GetReplicas(&member)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get replicas for member workload: %v", err)
		}
		memberMap[cluster].CurrentReplicas = current

		strategy, err = i.GetRollingStrategy(&member)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get rolling strategy for member workload: %v", err)
		}
		memberMap[cluster].RollingStrategy = strategy
		memberMap[cluster].SpecRevision = util.GetAnnotationValue(member.GetAnnotations(), workv1alpha2.ResourceTemplateTemplateHashAnnotationKey)
	}

	members := make([]*rollingContext, 0, len(clusterMap))
	for cluster, desired := range clusterMap {
		workload, ok := memberMap[cluster]
		if !ok {
			workload = &rollingContext{
				CurrentReplicas: 0,
				ClusterName:     cluster,
				SpecRevision:    updateRevision,
			}
		}
		if err = normalizeRollingContext(workload, federation, desired); err != nil {
			return nil, nil, fmt.Errorf("failed to set default unified workload: %v", err)
		}
		members = append(members, workload)
	}

	sort.Slice(members, func(i, j int) bool {
		if members[i].DesiredReplicas == members[j].DesiredReplicas {
			return members[i].ClusterName > members[j].ClusterName
		}
		return members[i].DesiredReplicas > members[j].DesiredReplicas
	})

	federationInfo, _ := json.Marshal(federation)
	klog.V(4).Infof("Calculated rolling context for %s/%s in federation: %s",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), string(federationInfo))

	membersInfo, _ := json.Marshal(members)
	klog.V(4).Infof("Calculated rolling context for %s/%s in memebrs: %s",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), string(membersInfo))
	return federation, members, nil
}
