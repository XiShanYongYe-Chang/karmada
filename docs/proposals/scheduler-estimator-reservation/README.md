---
title: 多集群负载调度支持资源预留
authors:
- TBD
reviewers:
- TBD
approvers:
- TBD

creation-date: 2026-03-30

---

# 多集群负载调度支持资源预留

## 概述

当多个使用 `Aggregated` 调度策略的工作负载被快速连续创建，且目标集群资源仅够承载其中一个时，
karmada-scheduler 可能将多个工作负载都调度到该集群，导致集群资源过度分配，部分 Pod 长期处于
Pending 状态。

根本原因在于：`karmada-scheduler-estimator` 基于成员集群当前状态快照进行资源计算，该快照无法
反映"飞行中"（已提交至 API Server 但尚未在集群内落地）的调度决策所消耗的资源。

本提案通过以下两个协作机制解决此问题：

1. **调度器侧飞行中资源预留缓存**：karmada-scheduler 在调度决策提交到 karmada-apiserver 后，
立即记录预留信息（含完整 `ReplicaRequirements`）。
2. **Estimator 节点模拟**：扩展 estimator 的 gRPC 接口，在 `MaxAvailableReplicas`
请求中携带飞行中的预留工作负载信息；estimator 在计算可用副本前，通过节点模拟算法逐副本将预留
工作负载"放置"到节点上，在节点维度精确扣减资源后，再计算当前请求的可用副本数。

## 动机

### 问题描述

karmada-scheduler 以单 Worker 串行方式处理调度队列，但其处理速度远快于成员集群的资源状态同步
速度。当两个 ResourceBinding 被快速连续调度时，会出现如下问题：

```
时间线    Scheduler Worker                        Estimator 快照（ClusterA）
T1      Binding1: MaxAvailableReplicas → 10      快照：0 个 Pod 已绑定节点，可用 10
T2      Binding1: 决策 → 分配 10 副本 → ClusterA
T3      Binding1: Patch karmada-apiserver ✓
T4      Binding2: MaxAvailableReplicas → 10      快照：仍为 0 个 Pod（informer 滞后！）
T5      Binding2: 决策 → 分配 10 副本 → ClusterA
T6      Binding2: Patch karmada-apiserver ✓      ClusterA 已超额承诺！
...
T+N s   Pod 在 ClusterA 内创建 → kube-scheduler 绑定节点 → Estimator informer 感知
```

Scheduler 在 T3 完成 Patch 后立即处理 Binding2，而此时 Binding1 产生的 Pod 甚至尚未在成员集群内
被创建，更不必说被 kube-scheduler 绑定至节点。Estimator 的节点快照只缓存已绑定节点的 Pod
（`pod.Spec.NodeName != ""`），因此在整个 informer 传播链路完成之前，Estimator 对 ClusterA 的
可用量评估始终与实际承诺量存在偏差。

两次调度在同一 informer 滞后窗口内完成，导致 ClusterA 被过度承诺。

### 目标

- 防止在多次调度决策重复占用同一集群的可用资源。
- 在 scheduler 进程内维护飞行中预留缓存，通过扩展 estimator gRPC 接口传递预留信息。
- Estimator 通过节点模拟，在节点维度精确扣减预留资源后计算可用副本数。
- 通过 TTL 机制保证预留缓存的自愈性，防止内存泄漏。

### 非目标

- 提供与 kube-scheduler 完全一致的节点选择结果（节点模拟是近似，karmada 不控制节点级调度）。
- 解决 `Duplicated` 调度模式的资源争用问题（该模式不涉及副本数计算）。
- 消除 API Server 与 estimator 节点快照之间的 informer 传播延迟。

## 提案

### 用户故事

#### 故事 1

作为平台运维人员，我同时创建两个使用 `Aggregated` 调度策略的 Deployment，目标集群只能承载其中
一个。我期望第二个 Deployment 能被调度到其他满足条件的集群（若存在），或收到明确的资源不足报
错——而不是两个都被调度到同一个集群，导致 Pod 静默 Pending。

#### 故事 2

作为开发者进行压测，我快速提交大量使用 `Aggregated` 策略的工作负载。我期望调度器在评估每个新
请求时，能正确扣除已承诺但尚未落地的工作负载所占用的集群资源。

## 设计详情

### 总体架构

```
karmada-scheduler（leader election，单活实例）
│
├── ClusterResourceReservationCache              【新增】
│     进程内：clusterName → bindingKey → {replicas, ReplicaRequirements, expiresAt}
│
├── calAvailableReplicas()                       【修改】
│     将当前集群的飞行中预留作为 reservedWorkloads 传入 estimator gRPC 请求
│
└── patchScheduleResultForResourceBinding()      【修改】
Patch 成功后 → delta = newReplicas - oldReplicas
delta > 0  → Reserve(cluster, bindingKey, delta, reqs)   // 新增飞行中副本
delta = 0  → Adjust(cluster, bindingKey, 0, newReqs)     // 刷新 requirements
delta < 0  → Adjust(cluster, bindingKey, delta, newReqs) // 减少飞行中副本数
集群移除   → Release(cluster, bindingKey)

event_handler.go                                【修改】
binding FullyApplied + 延迟 reservationReleaseDelay → Release(cluster, bindingKey)

karmada-scheduler-estimator
│
├── gRPC MaxAvailableReplicas()                 【修改：新增 reservedWorkloads 入参】
│
└── 节点模拟（新增）
for each reservedWorkload:
    Filter（亲和性 + 容忍 + 资源充足）→ LeastAllocated Score → 扣减选中节点
```

### 1. Scheduler 侧：ClusterResourceReservationCache

在 `pkg/scheduler/cache/` 中新增进程内预留缓存：

```go
// ReservedEntry 表示一条飞行中的调度预留记录。
type ReservedEntry struct {
    Replicas            int32
    // 完整的 ReplicaRequirements，含节点亲和性、容忍、资源请求等约束
    // estimator 模拟需要这些信息来正确 Filter 和扣减节点资源
    ReplicaRequirements workv1alpha2.ReplicaRequirements
    ExpiresAt           time.Time
}

// ClusterResourceReservationCache 跟踪已提交至 API Server 但尚未在成员集群内
// 真实落地的调度决策。并发安全（互斥锁保护），scheduler leader election 保证单活。
type ClusterResourceReservationCache struct {
    sync.RWMutex
    // clusterName → bindingKey → 预留条目
    items map[string]map[string]*ReservedEntry
    ttl   time.Duration
}
```

**方法：**

| 方法 | 调用时机 |
|------|----------|
| `Reserve(clusterName, bindingKey, replicas, ReplicaRequirements)` | Patch API Server 成功后，delta > 0 时调用；以 REPLACE 语义写入 |
| `Adjust(clusterName, bindingKey, delta, ReplicaRequirements)` | Patch API Server 成功后，delta <= 0 时调用；更新已有条目的 requirements 并按 delta 调整副本数 |
| `Release(clusterName, bindingKey)` | FullyApplied 后延迟 `reservationReleaseDelay` 触发；或集群从 placement 中移除时立即触发 |
| `GetReservedWorkloads(clusterName) []ReservedWorkload` | `calAvailableReplicas()` 构造 gRPC 请求时调用 |
| `GC()` | 后台每分钟执行一次 |

`GetReservedWorkloads` 返回指定集群下所有未过期预留条目，聚合为 estimator 所需的格式：

```go
func (c *ClusterResourceReservationCache) GetReservedWorkloads(clusterName string) []pb.ReservedWorkload {
    c.RLock()
    defer c.RUnlock()

    var result []pb.ReservedWorkload
    for _, entry := range c.items[clusterName] {
        if time.Now().After(entry.ExpiresAt) {
            continue
        }
        result = append(result, pb.ReservedWorkload{
            Replicas:            entry.Replicas,
            ReplicaRequirements: toPBReplicaRequirements(entry.ReplicaRequirements),
        })
    }
    return result
}
```

### 2. Scheduler 侧：修改 `calAvailableReplicas()`（`pkg/scheduler/core/util.go`）

将飞行中预留通过 gRPC 请求传递给 estimator，由 estimator 在节点维度处理：

```go
func calAvailableReplicas(clusters []*clusterv1alpha1.Cluster,
    spec *workv1alpha2.ResourceBindingSpec) []workv1alpha2.TargetCluster {
    // ...（现有逻辑不变）

    for name, estimator := range estimators {
        res, err := estimator.MaxAvailableReplicas(ctx, clusters, spec.ReplicaRequirements)
        // ...
    }
}
```

对应的 `SchedulerEstimator.MaxAvailableReplicas` 修改（`pkg/estimator/client/accurate.go`）：

```go
func (se *SchedulerEstimator) maxAvailableReplicas(ctx context.Context,
cluster string, replicaRequirements *workv1alpha2.ReplicaRequirements) (int32, error) {

    client, err := se.cache.GetClient(cluster)
    if err != nil {
        return UnauthenticReplica, err
    }

    req := &pb.MaxAvailableReplicasRequest{
        Cluster:             cluster,
        ReplicaRequirements: toPBReplicaRequirements(replicaRequirements),
        // 新增：从 scheduler 侧预留缓存获取飞行中预留，传入 estimator
        ReservedWorkloads: reservationCache.GetReservedWorkloads(cluster),
    }

    res, err := client.MaxAvailableReplicas(ctx, req)
    // ...
}
```

### 3. Scheduler 侧：修改 `patchScheduleResultForResourceBinding()`（`pkg/scheduler/scheduler.go`）

```go
    result, err := s.KarmadaClient.WorkV1alpha2().ResourceBindings(...).Patch(...)
    if err != nil {
        return err
    }

    // Patch 成功后立即更新预留，分三种情况处理：
    //
    //   delta > 0（新增副本，如首次部署或 scale-up）：
    //     使用 Reserve() 以 REPLACE 语义写入 delta 个飞行中副本。
    //     仅预留 delta 而非新总量，避免已运行副本（已在 Estimator 快照中扣减）被重复计算。
    //
    //   delta = 0（副本数不变，如资源规格更新）：
    //     使用 Adjust() 保持飞行中副本数不变，但刷新 ReplicaRequirements。
    //     这样 Estimator 模拟使用最新的资源规格，不会因 requirements 过时而偏差。
    //
    //   delta < 0（副本数减少，scale-down）：
    //     使用 Adjust() 按 delta 减少已有预留中的飞行中副本数（max 0），并刷新 requirements。
    //     被缩减的副本将被成员集群终止，无需继续预留；剩余部分仍在飞行中，需保留保护。
    //
    //   集群从新 placement 中移除（集群迁移）：
    //     调用 Release() 主动清除旧预留。
    //
    // delta = newReplicas - oldReplicas，oldReplicas 来自 Patch 前的 binding.Spec.Clusters。
    bindingKey := names.NamespacedKey(newBinding.Namespace, newBinding.Name)
    oldClusterMap := toClusterReplicaMap(oldBinding.Spec.Clusters)
    for _, cluster := range scheduleResult {
        oldReplicas := oldClusterMap[cluster.Name]
        delta := cluster.Replicas - oldReplicas
        if delta > 0 {
            s.schedulerCache.ClusterReservations().Reserve(
                cluster.Name,
                bindingKey,
                delta,
                newBinding.Spec.ReplicaRequirements,
            )
        } else {
            // delta <= 0：调整已有预留条目（若存在）
            // Adjust 语义：newInFlight = max(0, existing.Replicas + delta)
            //   newInFlight > 0 → 更新条目（replicas = newInFlight，requirements 刷新）
            //   newInFlight <= 0 → 删除条目（等价于 Release）
            s.schedulerCache.ClusterReservations().Adjust(
            cluster.Name,
            bindingKey,
            delta,
            newBinding.Spec.ReplicaRequirements,
            )
        }
    }
    // 集群迁移：释放已从新 placement 中移除的集群的旧预留
    newClusterSet := toClusterNameSet(scheduleResult)
    for clusterName := range oldClusterMap {
        if !newClusterSet.Has(clusterName) {
            s.schedulerCache.ClusterReservations().Release(clusterName, bindingKey)
        }
    }
```

### 4. Scheduler 侧：释放预留（`pkg/scheduler/event_handler.go`）

#### 释放时机的选择

预留何时释放是一个需要权衡的设计决策，核心问题是：**何时 Estimator 的节点快照能够自然反映该
工作负载的资源消耗，从而使预留不再必要？**

通过代码分析可知，Estimator 的 Pod informer 使用如下过滤条件：

```go
// assignedPod 仅缓存已被 kube-scheduler 绑定到节点的 Pod（pod.Spec.NodeName != ""）
func assignedPod(pod *corev1.Pod) bool {
    return len(pod.Spec.NodeName) != 0
}
```

这意味着：Pod 一旦被成员集群的 kube-scheduler 完成节点绑定（`NodeName` 写入），无论 Pod 是否
处于 Running 状态，其资源消耗即被计入 Estimator 节点缓存的 `Requested` 字段。

因此，预留可以安全释放的时机是：**成员集群 kube-scheduler 已将 Pod 绑定到节点，并且 Estimator
informer 已感知到该事件**。

以下三种方案可供选择：

**方案 A：`FullyApplied = True` + 固定延迟释放（本方案采用）**

`FullyApplied = True` 表示 Deployment manifest 已被成功 Apply 到成员集群的 kube-apiserver，
但此时 Pod 可能尚未被 kube-scheduler 完成节点绑定。引入固定延迟 `Δt`（默认 30s，可配置），
为成员集群 kube-scheduler 完成 Pod 绑定以及 Estimator informer 感知留出缓冲时间。

```
风险窗口：[FullyApplied, FullyApplied + Δt] 内如果 kube-scheduler 尚未完成绑定，
预留已释放而 Estimator 快照未更新，存在短暂保护缺失。
Δt 越大越安全，但预留持续时间越长，调度越保守。
```

**方案 B：仅依赖 TTL，不主动释放**

完全放弃主动 Release 逻辑，所有预留统一由 TTL 到期后 GC 清除。在 TTL 内预留始终存在，
Estimator 的贪心模拟始终扣减这部分资源，不存在任何窗口期。

```
优点：逻辑最简单，安全性最高，无窗口期风险。
缺点：预留保持时间较长（最长 TTL），对资源紧张场景的调度保守性有影响。
```

**方案 C：监听成员集群 Pod `NodeName` 写入（最精确）**

通过成员集群的 Pod informer 感知 Pod 已被绑定到节点，此时 Estimator 必然已或即将计入资源消耗，
可精确触发 Release。

```
优点：释放时机最准确，保护窗口最短。
缺点：需要 Karmada 控制面对成员集群 Pod 状态的感知能力，架构侵入性强，实现复杂度高。
```

**方案 D：检查 Workload Status 中的副本字段（参考）**

通过 ResourceBinding 的 `status.aggregatedStatus` 中采集回的工作负载状态来判断 Pod
是否已被调度至节点。以 Deployment 为例，可检查 `status.readyReplicas >= desired` 作为
释放信号。

```
优点：使用可观测的业务语义信号，避免经验性固定延迟。
缺点：
1. readyReplicas 要求 Pod Running + Ready，实际上比 NodeName 写入更晚，
Release 时机更保守而非更精确；
2. 不同资源类型（StatefulSet、Job、自定义资源）status 结构各异，
需要为每种类型实现独立的解析逻辑，工程复杂度高；
3. 依赖 status 采集链路（execution controller → status controller →
aggregatedStatus）完整工作，任何环节延迟均影响 Release 时机。
此方案可作为未来精细化优化的参考方向，不适合作为当前阶段的主路径。
```

> **开放讨论**：方案 B 最简单安全，方案 C 最精确，方案 A 是工程折中，方案 D 提供了业务语义
> 信号但引入额外复杂度。欢迎社区讨论最终采用的策略，以及方案 A 中 `Δt` 的合理默认值。

当前实现采用**方案 A**：

```go
func (s *Scheduler) onResourceBindingUpdate(_, new interface{}) {
    binding, ok := new.(*workv1alpha2.ResourceBinding)
    if !ok {
        return
    }
    // FullyApplied 表示 manifest 已 Apply 到成员集群 kube-apiserver。
    // 此时 Pod 可能尚未被 kube-scheduler 完成节点绑定，因此延迟 reservationReleaseDelay
    // 后再 Release，为 kube-scheduler 绑定 Pod 及 Estimator informer 感知留出缓冲。
    if meta.IsStatusConditionTrue(binding.Status.Conditions, workv1alpha2.FullyApplied) {
        bindingKey := names.NamespacedKey(binding.Namespace, binding.Name)
        time.AfterFunc(s.reservationReleaseDelay, func() {
            for _, cluster := range binding.Spec.Clusters {
                s.schedulerCache.ClusterReservations().Release(cluster.Name, bindingKey)
            }
        })
    }
}
```

`reservationReleaseDelay` 默认 30s，可通过 scheduler flag `--reservation-release-delay` 配置。(是否需要提供配置)

### 5. Estimator 侧：扩展 gRPC 接口（`pkg/estimator/pb/types.go`）

在 `MaxAvailableReplicasRequest` 中新增 `ReservedWorkloads` 字段：

```go
// MaxAvailableReplicasRequest 扩展：新增 ReservedWorkloads 字段。
type MaxAvailableReplicasRequest struct {
    // Cluster represents the cluster name.
    // +required
    Cluster string `json:"cluster" protobuf:"bytes,1,opt,name=cluster"`
    // ReplicaRequirements represents the resource and scheduling requirements for each replica.
    // +required
    ReplicaRequirements ReplicaRequirements `json:"replicaRequirements" protobuf:"bytes,2,opt,name=replicaRequirements"`
    // ReservedWorkloads represents the in-flight scheduling commitments that have been
    // patched to the API server but not yet reflected in the member cluster's real resource state.
    // The estimator performs greedy node simulation to account for these reservations
    // before computing the available replicas for the current request.
    // +optional
    ReservedWorkloads []ReservedWorkload `json:"reservedWorkloads,omitempty" protobuf:"bytes,3,rep,name=reservedWorkloads"`
}

// ReservedWorkload represents a single in-flight workload reservation.
// It contains the full scheduling constraints (affinity, tolerations, resource requests)
// required for the estimator to correctly simulate node placement.
type ReservedWorkload struct {
    // Replicas is the number of replicas committed by the in-flight scheduling decision.
    // +required
    Replicas int32 `json:"replicas" protobuf:"varint,1,opt,name=replicas"`
    // ReplicaRequirements contains the full resource and scheduling constraints of the reserved
    // workload. The estimator uses these to filter eligible nodes and simulate placement.
    // +required
    ReplicaRequirements ReplicaRequirements `json:"replicaRequirements" protobuf:"bytes,2,opt,name=replicaRequirements"`
}
```

同步更新 `.proto` 文件和生成代码：`hack/update-estimator-protobuf.sh`。

### 6. Estimator 侧：节点模拟算法

在 `pkg/estimator/server/estimate.go` 的 `EstimateReplicas` 中，在运行现有估算插件前，
先对节点快照执行模拟预处理：

```go
func (es *AccurateSchedulerEstimatorServer) EstimateReplicas(
    ctx context.Context, object string,
    request *pb.MaxAvailableReplicasRequest) (int32, error) {

    snapShot := schedcache.NewEmptySnapshot()
    if err := es.Cache.UpdateSnapshot(snapShot); err != nil {
        return 0, err
    }
    if snapShot.NumNodes() == 0 {
        return 0, nil
    }

    // 新增：对节点快照执行贪心模拟，扣减飞行中预留所占资源
    if len(request.ReservedWorkloads) > 0 {
        if err := es.simulateReservedPlacements(snapShot, request.ReservedWorkloads); err != nil {
            // 模拟失败不应阻断正常估算，记录日志后继续
            klog.Warningf("Failed to simulate reserved placements: %v, proceeding without reservation adjustment", err)
        }
    }

    maxAvailableReplicas, err := es.estimateReplicas(ctx, snapShot, request.ReplicaRequirements)
    // ...
}
```

**`simulateReservedPlacements` 模拟核心逻辑：**

```go
// simulateReservedPlacements 对每个飞行中的预留工作负载，逐副本通过 Filter + Score
// 选择最优节点，并在该节点上扣减资源，更新快照状态。
// 模拟完成后，快照反映了扣除预留后的节点实际可用资源。
func (es *AccurateSchedulerEstimatorServer) simulateReservedPlacements(
    snapshot *schedcache.Snapshot,
    reservedWorkloads []pb.ReservedWorkload) error {

    allNodes, err := snapshot.NodeInfos().List()
    if err != nil {
        return err
    }

    for _, reserved := range reservedWorkloads {
        for i := int32(0); i < reserved.Replicas; i++ {

            // Step 1 - Filter：找到满足硬约束（亲和性、容忍）且剩余资源充足的候选节点
            affinity := nodeutil.GetRequiredNodeAffinity(reserved.ReplicaRequirements)
            var tolerations []corev1.Toleration
            if reserved.ReplicaRequirements.NodeClaim != nil {
                tolerations = reserved.ReplicaRequirements.NodeClaim.Tolerations
            }
            var eligible []*schedulerframework.NodeInfo
            for _, node := range allNodes {
                if !nodeutil.IsNodeAffinityMatched(node.Node(), affinity) ||
                    !nodeutil.IsTolerationMatched(node.Node(), tolerations) {
                    continue
                }
                rest := node.Allocatable.Clone().SubResource(node.Requested)
                if rest.MaxDivided(reserved.ReplicaRequirements.ResourceRequest) < 1 {
                    continue // 资源不足，跳过
                }
                eligible = append(eligible, node)
            }
            if len(eligible) == 0 {
                // 无节点可承载，停止此工作负载后续副本的模拟
                break
            }

            // Step 2 - Score：LeastAllocated 策略，优先选剩余资源比例最高的节点
            // 与 kube-scheduler 默认策略一致，使模拟结果最接近真实调度
            bestNode := leastAllocatedNode(eligible)

            // Step 3 - 在选中节点上扣减资源（更新 Requested，体现预留消耗）
            bestNode.Requested.Add(reserved.ReplicaRequirements.ResourceRequest)
            bestNode.Requested.AllowedPodNumber++
        }
    }
    return nil
}

// leastAllocatedNode 返回所有候选节点中，已分配资源比例最低（剩余资源最多）的节点。
func leastAllocatedNode(nodes []*schedulerframework.NodeInfo) *schedulerframework.NodeInfo {
    var best *schedulerframework.NodeInfo
    var bestScore float64 = -1
    for _, node := range nodes {
        // score = (cpu_free/cpu_total + mem_free/mem_total) / 2，越高越优先
        score := leastAllocatedScore(node)
        if score > bestScore {
            bestScore = score
            best = node
        }
    }
    return best
}
```

**关键设计说明：**
- 每个预留副本只在**一个**被模拟选中的节点上扣减资源，其他节点完全不受影响，避免了对所有满足
约束节点广播扣减的过度保守问题。
- 通过更新节点的 `Requested` 字段（而非修改 `Allocatable`），与现有 `nodeMaxAvailableReplica`
计算逻辑（`Allocatable - Requested`）完全兼容，无需修改估算插件本身。
- Filter 复用了 estimator 现有的 `nodeutil.IsNodeAffinityMatched` 和 `IsTolerationMatched`
逻辑，与 `Estimate` 插件的过滤行为保持一致。
- Score 采用 LeastAllocated 策略，与 kube-scheduler 默认行为一致，模拟结果最接近真实调度。

### 7. 预留生命周期

```
    ResourceBinding 进入调度队列
    │
    ▼
    Algorithm.Schedule()
    calAvailableReplicas()
    estimator.MaxAvailableReplicas(
        clusters,
        replicaRequirements,
        reservedWorkloads,   ← 从 ClusterResourceReservationCache 获取
    )
    │
    ▼（estimator 侧）
    节点模拟：
    for each reservedWorkload → Filter → Score → 扣减选中节点
    在更新后的节点状态上计算 MaxAvailableReplicas
    │
    ▼（scheduler 侧）
    patchScheduleResultForResourceBinding()
    Patch API Server ✓
    ClusterReservations().Reserve(cluster, bindingKey, replicas, ReplicaRequirements)
    │
    ├──────────────────────────────────────────┐
    │                                          │
    ▼                                          ▼
    informer 收到 FullyApplied 事件            TTL 到期（5 分钟）
    等待 reservationReleaseDelay（默认 30s）   GC 清除条目
    ClusterReservations().Release()            │
    │                                          │
    └──────────────────────────────────────────┘
    │
    ▼
    预留移除；集群容量已完整反映在
    estimator 的下一次节点快照中
```

### 注意事项与约束

**Scheduler 单活保证内存状态一致性。**
karmada-scheduler 使用 leader election，任意时刻只有一个 scheduler 实例活跃。所有并发 Worker
goroutine 在同一进程内共享预留缓存（互斥锁保护），无需任何分布式协调。

**模拟精度本质上是近似值。**
karmada-scheduler 决定每个集群分配的副本数，但不控制副本在集群内具体落到哪个节点（这是成员集群
kube-scheduler 的职责）。模拟选节点的结果与 kube-scheduler 实际决策高度接近（使用相同的
LeastAllocated 策略），但不能完全等同。

**预留释放与 TTL 机制。**
预留在对应 ResourceBinding 达到 `FullyApplied` 条件后，延迟 `reservationReleaseDelay`
（默认 30s）释放，为成员集群 kube-scheduler 完成 Pod 节点绑定及 Estimator informer 感知留出
缓冲。所有条目同时设置 TTL（默认 5 分钟）兜底，防止因下游异常导致内存泄漏。

**Scheduler 重启后预留状态丢失。**
重启后进程内缓存清空，`ClusterResourceReservationCache` 不会随 informer 重新同步而重建。
在重启后到成员集群内 Pod 真正 Running 并被 Estimator 节点快照感知的时间窗口内，新的并发调度
可能看到陈旧的集群容量，存在短暂超额分配风险。随着 Pod 陆续 Running，Estimator 快照自然反映
实际资源消耗，问题自愈。这是概率较低的边界场景，属于可接受的工程权衡。

### 风险与应对

| 风险 | 应对措施 |
|------|----------|
| 模拟过保守导致合法工作负载被拒绝 | 每个副本只在一个被选中节点上扣减资源，其余节点不受影响 |
| 预留释放过早导致保护窗口期 | FullyApplied 后延迟 30s 释放，覆盖 kube-scheduler 绑定 Pod 及 Estimator informer 感知时间；TTL 兜底 |
| Scheduler 重启丢失飞行中的预留 | 重启后 Pod 陆续 Running，Estimator 快照自然反映实际消耗，自动自愈；属于可接受的工程权衡 |
| gRPC 请求体因预留信息变大导致性能下降 | 预留条目数量有限（TTL 兜底），单次请求体可控 |

### 测试计划

**单元测试：**
- `ClusterResourceReservationCache`：Reserve/Release/GetReservedWorkloads/GC、TTL 过期、
并发安全性。
- `simulateReservedPlacements`：
- 单一预留副本正确选节点并扣减资源
- 多个预留副本依次模拟放置（验证 LeastAllocated 打散行为）
- 候选节点资源耗尽时中断模拟
- 预留工作负载有亲和性/容忍约束时的 Filter 逻辑
- 端到端 `EstimateReplicas`：有 reservedWorkloads 时结果正确调整；无 reservedWorkloads 时
行为与改动前完全一致（向后兼容）。

**E2E 测试：**
- 同时创建两个 `Aggregated` 策略的 Deployment，目标集群资源仅够承载一个；断言仅一个成功调度，
另一个要么被调度到其他集群，要么产生明确的资源不足事件，不出现 Pod 静默 Pending。

## 备选方案

### 备选方案一：调度成功后通知 Estimator 预留资源

**描述：** 调度决策成功后，scheduler 向 estimator 发送额外的 gRPC 调用，通知其预留已分配的
资源（附带 TTL 自动释放）。

**分析：**

`karmada-scheduler-estimator` 是**无状态的计算服务**：每次 `MaxAvailableReplicas` gRPC 调用
独立地基于成员集群 informer 的当前快照进行计算，调用之间没有任何共享状态。

Estimator 以 HA 模式运行（多副本同时提供服务，无 leader election），多个副本同时提供服务，无
leader election。Scheduler 与 estimator 之间通过 Kubernetes Service 建立 gRPC 连接，请求
会被负载均衡到任意副本。

若 Reserve 调用落在副本 A，而后续的 `MaxAvailableReplicas` 调用落在副本 B，副本 B 对该预留
一无所知——各副本之间没有共享状态。要使此方案可行，需要引入外部共享存储（Redis、etcd）或副本间
Gossip 协议，将 estimator 从**无状态服务**变为**有状态服务**，与其核心设计哲学根本矛盾。

### 备选方案二：仅在 Scheduler 侧做集群层面整体扣减（精度不足）

**描述：** Scheduler 侧维护预留缓存，调用 estimator 后在集群层面减去等效副本数，不修改
estimator 接口。

**分析：**

集群层面整体扣减在资源画像差异较大时精度不足。Scheduler 不知道预留资源具体落在哪个节点，只能
对整体做保守估算（ceiling division，向上取整除法）：

```
ClusterA: Node1(10CPU可用), Node2(2CPU可用)，每副本需要 3CPU
estimator 结果: floor(10/3) + floor(2/3) = 3 + 0 = 3

已预留 1 个副本（3CPU，实际落在 Node1）：
- 真实可用: floor(7/3) + floor(2/3) = 2 + 0 = 2
- 本方案:   equiv = ceil(3/3) = 1，调整后 = 3 - 1 = 2  ✓（此例恰好准确）

已预留 1CPU（同属 Node1）：
- 真实可用: floor(9/3) + floor(2/3) = 3 + 0 = 3
- 本方案:   equiv = ceil(1/3) = 1，调整后 = 3 - 1 = 2  ✗（过于保守，少算了 1）
```

预留工作负载与当前请求的资源画像差异越大，误差越明显。

此方案可作为模拟不可用时的降级（fallback）策略，例如 estimator 版本不支持新接口时。在这种
场景下，scheduler 可检测 estimator 是否支持 `ReservedWorkloads` 字段，若不支持则回退到集群层面
扣减。

### 备选方案三：对所有满足约束的节点各自扣减（过度保守）

**描述：** 将预留工作负载的资源量从每个满足其亲和性/容忍约束的节点上各自扣减。

**分析：**

每个副本只会落在**一个**节点上，但此方案对所有候选节点都进行扣减，导致严重的过保守：

```
Node1、Node2、Node3 均满足约束，已预留 1 个副本（3CPU）
此方案：每个节点都扣 3CPU → Node1: 7CPU, Node2: 7CPU, Node3: 7CPU → 可用 = 2+2+2 = 6
真实情况（副本只落 1 个节点）：
Node1: 7CPU, Node2: 10CPU, Node3: 10CPU → 可用 = 2+3+3 = 8
```

候选节点越多，误差倍增，不可用。

### 扣减精度对比

| 方案 | 误差来源 | 结果偏向 |
|------|----------|----------|
| 备选二：集群层面整体扣减 | 资源画像不同时估算偏差 | 偏保守 |
| 备选三：对所有满足约束节点各自扣减 | 一个副本只落一个节点，却对所有节点都扣 | 节点越多越保守 |
| **本方案：节点模拟** | 贪心选节点 ≠ kube-scheduler 实际决策 | **最接近真实** |
| 理论最优（已知精确落节点） | — | 当前架构层面不可达 |

节点模拟是当前 karmada 架构下（不控制 pod 在成员集群内的 node 级调度）**精度上限最高的可行方案**。

## 未来扩展

### 节点 Filter 与 Score 插件扩展点

当前模拟的 Filter 逻辑（亲和性、容忍、资源充足性）和 Score 策略（LeastAllocated）均以
硬编码方式内置在 `simulateReservedPlacements` 中。未来可将其抽象为可插拔的扩展点，以适配不同
成员集群的 kube-scheduler 配置。

`pkg/estimator/server/framework/interface.go` 中已预留了对应的 TODO 注释：

```go
// TODO(wengyao04): we can add filter and score plugin extension points if needed in the future
```

扩展方向：
- **`NodeFilterPlugin`**：抽象节点过滤逻辑，内置 `NodeResourceFilter`、
`NodeAffinityAndTolerationFilter`，用户可自定义。
- **`NodeScorePlugin`**：抽象节点打分策略，内置 `LeastAllocatedScorer`（默认，对应
kube-scheduler 默认行为）和 `MostAllocatedScorer`（对应 bin-packing 配置），通过
estimator 启动参数选择，以匹配成员集群实际的调度策略。

该扩展不影响本方案的核心功能，可作为独立的后续增强交付。
