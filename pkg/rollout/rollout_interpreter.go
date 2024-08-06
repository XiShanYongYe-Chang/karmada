package rollout

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
type statelessRolloutInterpreterImpl struct { // 这个实现是通用的还是转为stateless设计的呢？
	resourceInterpreter resourceinterpreter.ResourceInterpreter
}

// NewAdapterRolloutInterpreter creates a new Rollout Interpreter for given workload type.
func NewAdapterRolloutInterpreter(interpreter resourceinterpreter.ResourceInterpreter) Interpreter {
	// TODO: support stateful rolling interpreter type
	return &statelessRolloutInterpreterImpl{resourceInterpreter: interpreter}
}

// AlignRollingStrategy aligns the rolling strategy for each member based on the given resource template and workloads status.
func (i *statelessRolloutInterpreterImpl) AlignRollingStrategy(resourceTemplate *unstructured.Unstructured, workloads map[string]*unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (map[string]*unstructured.Unstructured, error) {
	clusterRolloutStrategies, err := i.calculateRollingStrategyForEachCluster(resourceTemplate, clusters, works)
	if err != nil {
		return nil, err
	}

	revisedWorkloads := make(map[string]*unstructured.Unstructured, len(workloads))
	for clusterName, workload := range workloads {
		revisedWorkloads[clusterName], err = i.resourceInterpreter.ReviseRollingStrategy(workload, clusterRolloutStrategies[clusterName])
		if err != nil {
			klog.Errorf("Failed to revise workload(%s/%s /%s/%s) under cluster(%s), error: %v",
				workload.GetAPIVersion(), workload.GetKind(), workload.GetNamespace(), workload.GetName(), clusterName, err)
			return nil, err
		}
	}
	return revisedWorkloads, nil
}

// calculateRollingStrategyForEachCluster calculates the rolling strategy for each member based on the given resource template and workloads status.
func (i *statelessRolloutInterpreterImpl) calculateRollingStrategyForEachCluster(resourceTemplate *unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (map[string]*configv1alpha1.RollingStrategy, error) {
	klog.V(4).Infof("Begin to calculate rollingStrategy for resource(%s/%s %s/%s).",
		resourceTemplate.GetAPIVersion(), resourceTemplate.GetKind(), resourceTemplate.GetNamespace(), resourceTemplate.GetName())
	/*
		@@ Calculate normalized rolling context for federationContext and memberContexts. @@
	*/
	federationContext, memberContexts, err := i.calculateRollingContext(resourceTemplate, clusters, works)
	if err != nil {
		klog.Errorf("Failed to calculate rollingStrategy contexts for resource(%s/%s): %v",
			resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
		return nil, err
	}

	memberContexts = calculatePartitionsForEachCluster(federationContext, memberContexts, works)
	memberContexts = calculateMaxUnavailableForEachCluster(federationContext, memberContexts)
	memberContexts = calculateMaxSurgeForEachCluster(federationContext, memberContexts)

	if err = checkRollingStrategyAligned(federationContext, memberContexts); err != nil {
		// just warning it, do not return this error.
		klog.Warningf("Rolling strategy is not aligned for resource(%s/%s): %v",
			resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
	}

	// correct the invalid rolling strategy.
	for _, member := range memberContexts {
		// Most stateless workloads(e.g., Deployment/CloneSet) require that maxUnavailable and maxSurge
		// must not be both 0, otherwise validation admission webhook will deny the create/update request.
		if member.RollingStrategy.MaxSurge.IntVal == 0 && member.RollingStrategy.MaxUnavailable.IntVal == 0 {
			// Hack: We set maxSurge=1 to ensure the update can be allowed by the validation admission,
			// and ensure NO ONE replicas will be updated due to `member.partition = member.replicas`
			// or `member.paused = true`.
			member.RollingStrategy.MaxSurge.IntVal = 1
			member.RollingStrategy.Partition.IntVal = member.DesiredReplicas // 这样不会让所有成员集群的partition之和超过联邦的partition么？
			member.RollingStrategy.Paused = pointer.Bool(true)               // 这个逻辑没被使用到
		}
	}

	strategies := make(map[string]*configv1alpha1.RollingStrategy, len(memberContexts))
	for _, member := range memberContexts {
		strategies[member.ClusterName] = member.RollingStrategy
	}

	strategyInfo, _ := json.Marshal(strategies) // 直接输出是不是可以跳过marshal
	klog.V(4).Infof("Calculated rolling strategies for resource(%s/%s) in memberContexts: %v",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), string(strategyInfo))
	return strategies, nil
}

// calculateRollingContext calculates the rolling context for federation and members.
func (i *statelessRolloutInterpreterImpl) calculateRollingContext(resourceTemplate *unstructured.Unstructured, clusters []workv1alpha2.TargetCluster, works map[string]*workv1alpha1.Work) (*rollingContext, []*rollingContext, error) {
	fedRollingContext, err := i.calculateFederationRollingContext(resourceTemplate)
	if err != nil {
		klog.Errorf("Failed to calculate federation rollingStrategy contexts for resource(%s/%s): %v",
			resourceTemplate.GetNamespace(), resourceTemplate.GetName(), err)
	}

	clusterReplicas := make(map[string]int32, len(clusters))
	for _, cluster := range clusters {
		clusterReplicas[cluster.Name] = cluster.Replicas
	}

	rollingContextMap := make(map[string]*rollingContext, len(clusters))
	for clusterName, work := range works {
		if _, ok := clusterReplicas[clusterName]; !ok {
			continue
		}

		rollingContextMap[clusterName] = &rollingContext{ClusterName: clusterName}

		workload := unstructured.Unstructured{}
		if err = json.Unmarshal(work.Spec.Workload.Manifests[0].Raw, &workload); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal workload from work: %v", err)
		}

		unifiedStatus := &configv1alpha1.UnifiedRollingStatus{}
		if len(work.Status.ManifestStatuses) > 0 {
			unifiedStatus, err = i.resourceInterpreter.InterpretRollingStatus(&workload, work.Status.ManifestStatuses[0].Status)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to interpret rolling status: %v", err)
			}
		}

		if !isWorkloadGenerationConsistent(&workload, unifiedStatus) {
			// 此处 return 返回 error 是不是不太合适，这意味着会重新reconcile。是不是退出不重试比较好呢？
			return nil, nil, fmt.Errorf("current work mainifest has not been reconciled in cluster(%s)", clusterName)
		}

		//unifiedStatus = normalizeRollingStatus(unifiedStatus)
		rollingContextMap[clusterName].Status = unifiedStatus

		currentReplicas, _, err := i.resourceInterpreter.GetReplicas(&workload)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get replicas for workload: %v", err)
		}
		rollingContextMap[clusterName].CurrentReplicas = currentReplicas

		currentRollingStrategy, err := i.resourceInterpreter.GetRollingStrategy(&workload)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get rolling rollingStrategy for workload: %v", err)
		}
		rollingContextMap[clusterName].RollingStrategy = currentRollingStrategy
		rollingContextMap[clusterName].SpecRevision = util.GetAnnotationValue(workload.GetAnnotations(), workv1alpha2.ResourceTemplateTemplateHashAnnotationKey)
	}

	updateRevision := util.GetAnnotationValue(resourceTemplate.GetAnnotations(), workv1alpha2.ResourceTemplateTemplateHashAnnotationKey)
	memberRollingContexts := make([]*rollingContext, 0, len(clusterReplicas))
	for cluster, desiredReplicas := range clusterReplicas {
		memberContext, ok := rollingContextMap[cluster]
		if !ok {
			memberContext = &rollingContext{
				CurrentReplicas: 0,
				ClusterName:     cluster,
				SpecRevision:    updateRevision,
			}
		}
		if memberContext, err = normalizeRollingContext(memberContext, fedRollingContext, desiredReplicas); err != nil {
			return nil, nil, fmt.Errorf("failed to set default unified workload: %v", err)
		}
		memberRollingContexts = append(memberRollingContexts, memberContext)
	}

	sort.Slice(memberRollingContexts, func(i, j int) bool {
		if memberRollingContexts[i].DesiredReplicas != memberRollingContexts[j].DesiredReplicas {
			return memberRollingContexts[i].DesiredReplicas > memberRollingContexts[j].DesiredReplicas
		}
		return memberRollingContexts[i].ClusterName > memberRollingContexts[j].ClusterName
	})

	klog.V(4).Infof("Calculated rolling context for %s/%s in members: %+v",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), memberRollingContexts)
	return fedRollingContext, memberRollingContexts, nil
}

func (i *statelessRolloutInterpreterImpl) calculateFederationRollingContext(resourceTemplate *unstructured.Unstructured) (*rollingContext, error) {
	replicas, _, err := i.resourceInterpreter.GetReplicas(resourceTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to get replicas: %v", err)
	}

	rollingStrategy, err := i.resourceInterpreter.GetRollingStrategy(resourceTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to get rolling rollingStrategy: %v", err)
	}

	rollingStrategy, err = normalizeRollingStrategy(rollingStrategy, replicas)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize rollingStrategy: %v", err)
	}

	// MaxUnavailable and MaxSurge should not be both 0,
	// we should ensure at least one of replicas can be updated.
	// 会出现这种情况么，允许用户这么设置么？
	if replicas > 0 && rollingStrategy.MaxUnavailable.IntVal == 0 && rollingStrategy.MaxSurge.IntVal == 0 {
		rollingStrategy.MaxUnavailable.IntVal = 1
	}

	updateRevision := util.GetAnnotationValue(resourceTemplate.GetAnnotations(), workv1alpha2.ResourceTemplateTemplateHashAnnotationKey)
	fedRollingContext := &rollingContext{
		DesiredReplicas: replicas,
		RollingStrategy: rollingStrategy,
		SpecRevision:    updateRevision,
	}
	klog.V(4).Infof("Calculated federation rollingStrategy context for resource(%s/%s): %+v",
		resourceTemplate.GetNamespace(), resourceTemplate.GetName(), fedRollingContext)
	return fedRollingContext, nil
}

func calculatePartitionsForEachCluster(federationContext *rollingContext, memberContexts []*rollingContext, works map[string]*workv1alpha1.Work) []*rollingContext {
	// updatedReplicasQuota indicate the number of **oldRevision** replicas
	// that can be updated in federated level according to `partition`.
	//
	// initialized by `federationContext.replicas - federationContext.partition - SUM(memberContext.replicasMember.partition)`
	// 为什么还需要减去后边的SUM？这个注释感觉是错误，正确的感觉是这样的 `SUM(memberContext.replicasMember.partition) - federationContext.partition`
	var updatedReplicasQuota = federationContext.DesiredReplicas - federationContext.RollingStrategy.Partition.IntVal
	for _, memberContext := range memberContexts {
		// The corresponding work is not applied means all of it revision must be update revision.
		// All replicas of this memberContext will be updated.
		if _, ok := works[memberContext.ClusterName]; !ok {
			updatedReplicasQuota -= memberContext.DesiredReplicas
			continue
		}

		// correct the partition for this memberContext. 为什么需要修正一下？会出现这种情况么
		memberContext.RollingStrategy.Partition.IntVal = integer.Int32Min(memberContext.RollingStrategy.Partition.IntVal, memberContext.OldRevisionReplicas())

		// We correct the current partition for this memberContext.
		if memberContext.RollingStrategy.Partition.IntVal > 0 {
			// maxUpdatedReplicasPossible is the maximum number of replicas that this workload may update.
			// current Partition should equal to `target.Replicas - maxUpdatedReplicasPossible`, which means
			// we expected the newly-scaled-up replicas is the old revision as much as possible if
			// `target.Replicas > currentReplicas`.
			// 这块儿计算是不是有点儿问题，当scale-down时，甚至会出现partition大于replicas的情况？
			// rollout时还能出现scale的情况么，当scale-up的时候，新增的不应该是新版本的么？
			maxUpdatedReplicasPossible := integer.Int32Max(memberContext.CurrentReplicas-memberContext.RollingStrategy.Partition.IntVal, 0)
			maxUpdatedReplicasPossible = integer.Int32Min(maxUpdatedReplicasPossible, memberContext.DesiredReplicas)
			partitionedReplicasPossible := integer.Int32Max(memberContext.DesiredReplicas, memberContext.CurrentReplicas) - maxUpdatedReplicasPossible
			memberContext.RollingStrategy.Partition = &intstr.IntOrString{Type: intstr.Int, IntVal: partitionedReplicasPossible}
		}

		if memberContext.SpecRevision == federationContext.SpecRevision {
			// - For new-revision workload, we should consider it have been updated partially.
			updatedReplicasQuota -= memberContext.DesiredReplicas - int32(memberContext.RollingStrategy.Partition.IntValue())
		} else {
			// - For old-revision workload, we should assume it is fully partitioned.
			memberContext.RollingStrategy.Partition.IntVal = memberContext.DesiredReplicas
		}
	}

	if updatedReplicasQuota <= 0 {
		return memberContexts
	}

	// memberContext.partition += MIN(updatedReplicasQuota, memberContext.currentUnavailable)
	//
	// This loop ensure update the old unavailable replicas firstly.
	for _, member := range memberContexts {
		if updatedReplicasQuota == 0 {
			break
		}

		updatedUnavailable := member.UpdatedUnavailableReplicas()
		currentUnavailable := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas-updatedUnavailable, 0) // 为什么使用DesiredReplicas，而不是CurrentReplicas
		updatedReplicasQuotaToAdd := integer.Int32Min(member.RollingStrategy.Partition.IntVal, updatedReplicasQuota)
		updatedReplicasQuotaToAdd = integer.Int32Min(updatedReplicasQuotaToAdd, currentUnavailable)
		member.RollingStrategy.Partition.IntVal -= updatedReplicasQuotaToAdd
		updatedReplicasQuota -= updatedReplicasQuotaToAdd
	}

	if updatedReplicasQuota <= 0 {
		return memberContexts
	}

	// memberContext.partition += MIN(updateQuota, memberContext.current)
	//
	// This loop ensure sum of memberContext target update replicas equals to federationContext's.
	for _, member := range memberContexts {
		if updatedReplicasQuota == 0 {
			break
		}
		// the number of next updated replicas in this memberContext cluster should
		// not exceed the target updated replicas at federated level.
		updatedReplicasQuotaToAdd := integer.Int32Min(member.RollingStrategy.Partition.IntVal, updatedReplicasQuota)
		member.RollingStrategy.Partition.IntVal -= updatedReplicasQuotaToAdd
		updatedReplicasQuota -= updatedReplicasQuotaToAdd
	}

	return memberContexts
}

func calculateMaxUnavailableForEachCluster(federationContext *rollingContext, memberContexts []*rollingContext) []*rollingContext {
	// updateTargetQuota indicate the number of **old-revision && ready** replicas
	// that can be updated in federated level according to `maxUnavailable`.
	//
	// initialized by federationContext.maxUnavailable - SUM(member.unavailable)
	var unavailableQuota = federationContext.RollingStrategy.MaxUnavailable.IntVal
	for _, member := range memberContexts {
		unavailableReplicas := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas, 0)
		unavailableQuotaToAdd := integer.Int32Min(unavailableReplicas, unavailableQuota)
		member.RollingStrategy.MaxUnavailable.IntVal = unavailableQuotaToAdd
		unavailableQuota -= unavailableQuotaToAdd
	}

	if unavailableQuota <= 0 {
		return memberContexts
	}

	// member.maxUnavailable += MIN(unavailableQuota, member.current_available_to_update)
	//
	// This loop ensure not exceed federationContext.maxUnavailable by controlling the number of available replicas to update.
	for _, member := range memberContexts {
		if unavailableQuota == 0 {
			break
		}

		unavailableReplicas := integer.Int32Max(member.DesiredReplicas-*member.Status.AvailableReplicas, 0)
		maxUpdatedPossibleReplicas := integer.Int32Max(member.DesiredReplicas-member.RollingStrategy.Partition.IntVal, 0)
		toUpdateReplicas := integer.Int32Max(maxUpdatedPossibleReplicas-*member.Status.UpdatedReplicas, 0)

		var unavailableQuotaToAdd int32
		if member.Status.UpdatedReadyReplicas != nil {
			// more accurate calculation for such workload reporting its updated ready replicas.
			updatedUnavailableReplicas := integer.Int32Max(*member.Status.UpdatedReplicas-*member.Status.UpdatedReadyReplicas, 0)
			toUpdateUnavailableReplicas := integer.Int32Max(unavailableReplicas-updatedUnavailableReplicas, 0)
			toUpdateAvailableReplicas := integer.Int32Max(toUpdateReplicas-toUpdateUnavailableReplicas, 0)
			unavailableQuotaToAdd = integer.Int32Min(toUpdateAvailableReplicas, unavailableQuota)
		} else {
			maxAvailableReplicas := integer.Int32Max(member.DesiredReplicas-unavailableReplicas, 0)
			unavailableQuotaToAdd = integer.Int32Min(toUpdateReplicas, unavailableQuota)
			unavailableQuotaToAdd = integer.Int32Min(unavailableQuotaToAdd, maxAvailableReplicas) // 为什么需要加这个判断呢？
		}
		member.RollingStrategy.MaxUnavailable.IntVal += unavailableQuotaToAdd
		unavailableQuota -= unavailableQuotaToAdd
	}

	if unavailableQuota <= 0 {
		return memberContexts
	}

	// member.maxUnavailable += FLOOR(unavailableQuota * member.replicas / federationContext.replicas)
	//
	// The rest unavailableQuota will not determinate the number of updated replicas, so
	// we just distribute the rest unavailableQuota to each member cluster by replicas weight
	// to ensure the sum of maxUnavailable is equal to federationContext.maxUnavailable as much as possible.
	totalRestUnavailableQuota := unavailableQuota
	for _, member := range memberContexts {
		unavailableQuotaToAdd := int32(math.Floor(float64(totalRestUnavailableQuota*member.DesiredReplicas) / float64(federationContext.DesiredReplicas)))
		unavailableQuotaToAdd = integer.Int32Min(unavailableQuotaToAdd, unavailableQuota)
		member.RollingStrategy.MaxUnavailable.IntVal += unavailableQuotaToAdd
		unavailableQuota -= unavailableQuotaToAdd
	}

	// handle rest unavailableQuota cased by round down, will not execute
	// more than len(memberContexts) times in the following loops.
	currentIndex, totalIndex := 0, len(memberContexts)
	for totalIndex > 0 && unavailableQuota > 0 {
		memberContexts[currentIndex].RollingStrategy.MaxUnavailable.IntVal += 1
		currentIndex = (currentIndex + 1) % totalIndex
		unavailableQuota--
	}

	return memberContexts
}

func calculateMaxSurgeForEachCluster(federationContext *rollingContext, memberContexts []*rollingContext) []*rollingContext {
	// updateTargetQuota indicate the number of replicas
	// that can be surged in federated level according to `maxSurge`.
	//
	// initialized by federationContext.maxSurge
	var surgeQuota = federationContext.RollingStrategy.MaxSurge.IntVal

	// member.maxSurge = MIN(surgeQuota, member.current_to_update)
	//
	// This loop ensure not exceed federationContext.maxSurge by controlling the number of replicas to surge,
	// and ensure the surge replicas is effective as much as possible.
	for _, member := range memberContexts {
		if surgeQuota == 0 {
			break
		}
		currentRevisionReplicas := integer.Int32Max(*member.Status.Replicas-*member.Status.UpdatedReplicas, 0)
		currentToUpdateReplicas := integer.Int32Max(currentRevisionReplicas-member.RollingStrategy.Partition.IntVal, 0)
		surgeQuotaToAdd := integer.Int32Min(currentToUpdateReplicas, surgeQuota)
		member.RollingStrategy.MaxSurge.IntVal = surgeQuotaToAdd
		surgeQuota -= surgeQuotaToAdd
	}

	return memberContexts
}
