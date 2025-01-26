---
title: Optimize Cluster Failover
authors:
- "@XiShanYongYe-Chang"
reviewers:
- "@robot"
- TBD
approvers:
- "@robot"
- TBD

creation-date: 2025-01-10

---

# Optimize Cluster Failover

Failover 特性在下文中特指 Cluster Failover，对于应用 Failover 来说，我们用 Application Failover 来表示。

[Failover](https://karmada.io/docs/next/userguide/failover/failover-overview) 特性用于提高多集群场景下业务的可用性。当用户将 Failover 特性开关打开之后，便可以享受 Failover 特性带来的功能体验。作为一个重量级的特性，其体验需要不断打磨提升，给用户带来更佳的体验效果，现在我们认为其第一次的体验提升时机到了。

## Summary

![](.\optimize-cluster-failover-overview.png)

本次 Cluster Failover 优化将原有的 Failover 流程阶段化、模块化，原有的流程主要可以划分为两个阶段：Failover 检测阶段与 Failover 执行阶段，每个阶段的详细设计内容在后续展开。

## Motivation

在之前的版本中，Failover 由一个全局的 Failover FeatureGate 进行控制，将该 FeatureGate 设置为 true 之后，表示启用 Failover 功能。当某个成员集群被判定为 NotReady 之后，Karmada 系统会为其打上 NotExecute 的污点，进而触发该集群上所有资源的驱逐与迁移。这整个 Failover 过程完全自动化，用户无法进行干涉并且也无法限制受影响的资源范围。

通过收集社区用户的使用反馈，识别到 Failover 特性对用户整体业务的影响较大，当集群故障之后，会触发集群上所有资源进行迁移，包括非负载类资源（如 Configmap、Secret）、负载类资源（如 Deployment、StatefulSet）及 CRD 资源，用户无法介入迁移流程、确认故障实情，并控制资源迁移范围。以上情况，触成了本次 Failover 特攻优化的契机。

### Goals

1、将 Failover 的流程阶段化：检测阶段与执行阶段；
2、在 Failover 检测阶段中提供用户可配置的行为，来决定集群的何种状态条件才能为集群添加 NoExecute 的污点，进而触发 Failover 的执行；
3、在 Failover 执行阶段中明确负载具有驱逐资格的检查条件并加以执行；
4、明确 Failover 执行阶段各个模块的目的与方案设计。

### Non-Goals

1、在 Failover 检测阶段，仍使用原有的集群健康检查方式，不对集群状态检测方式进行扩展。

## Proposal

### User Stories (Optional)

#### Story 1

作为管理人员，希望能够提升管理和运维体验，控制单个负载应用在集群故障之后的迁移行为，不同资源对集群故障的应对措施相互独立，从而防止单个集群故障之后，集群上的所有资源无差别迁移，对现有业务造成冲击。

### Notes/Constraints/Caveats (Optional)

### Risks and Mitigations

## Design Details

Failover 流程将分为**检测**和**执行**两个阶段，检测阶段包括集群故状态检测与污点处理，执行阶段包括驱逐资格审查、预迁移、迁移、驱逐。

### 检测阶段

在检测阶段，首先对集群状态进行收集，集群状态最终体现在 Cluster 对象中的 `.status.conditions` 字段上。在 Karmada 控制器中，`cluster-status-controller` 通过访问成员集群 `kube-apiserver` 组件的健康检查接口来判断集群的健康状态，当前提案仍使用该检测方式，不对其进行扩展。除此之外，用户也可以通过主动上报的方式，增加或修改 Cluster 对象 `.status.conditions` 中的目标 Condition，来反应集群的状态信息；或者，用户可以部署额外的第三方检测工具，对集群状态进行检测并反映到 Cluster 对象的 `.status.conditions` 字段中。

```yaml
apiVersion: cluster.karmada.io/v1alpha1
kind: Cluster
metadata:
    name: cluster-x
spec:
    apiEndpoint: https://x.x.x.x:x
status:
    conditions:
    - lastTransitionTime: "ttt"
      message: xx
      reason: xx
      status: sss
      type: XXX
    - ...
```
#### API 定义

本提案计划新增一个**污点处理**模块，它负责监听集群状态条件的变化，即 Cluster 对象的 `.status.conditions` 字段，然后根据用户定义的污点处理策略 API 来决定如何修改 Cluster 对象的污点。污点处理模块包含三要素：触发条件（cluster status condition）、主体（集群对象）、污点。这类似于 [Remedy](.\..\remediation\cluster-status-based-remedy.md) 特性，由用户自定义 Remedy API，当触发条件完全匹配之后给目标集群添加指定污点。

```yaml
apiVersion: cluster.karmada.io/v1alpha1
kind: Cluster
metadata:
    name: cluster-x
spec:
    apiEndpoint: https://x.x.x.x:x
    taint:
    - effect: NoExecute
      key: cluster.karmada.io/not-ready
      timeAdded: "ttt"
```

新增 TaintDetector CRD 用于描述由集群状态条件驱动的集群污点管理策略。该资源是 Cluster scope 资源，它的组是 `policy.karmada.io`，版本是 `v1alpha1`。

```go
type TaintDetector struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    // Spec represents the desired behavior of TaintDetector.
    // +required
    Spec TaintDetectorSpec `json:"spec"`
}

type TaintDetectorSpec struct {
    // ClusterAffinity specifies the clusters that TaintDetector needs to pay attention to.
    // For clusters that meet the DecisionConditions, Actions will be preformed.
    // If empty, all clusters will be selected.
    // +optional
    ClusterAffinity *ClusterAffinity `json:"clusterAffinity,omitempty"`

    // DecisionControls indicates the decision matches of triggering
    // the taint system to add taints on the cluster object.
    // As long as any one DecisionControl matches, the TaintsToAdd will be added.
    // If empty, the TaintsToAdd will be added immediately.
    // +optional
    DecisionMatches []DecisionMatch `json:"decisionMatches,omitempty"`

    // TaintsToAdd specifies the NoExecute effect taints that
    // need to be applied to the clusters which match with ClusterAffinity.
    // +optional
    TaintsToAdd []TaintToAdd `json:"taintsToAdd,omitempty"`
}

// DecisionMatch represents the decision match detail of activating the taint system.
type DecisionMatch struct {
    // ClusterConditionMatch describes the cluster condition requirement.
    // +optional
    ClusterConditionMatch *ClusterConditionRequirement `json:"clusterConditionMatch,omitempty"`
}

// ClusterConditionRequirement describes the Cluster condition requirement details.
type ClusterConditionRequirement struct {
    // ConditionType specifies the ClusterStatus condition type.
    ConditionType string `json:"conditionType"`

    // Operator represents a conditionType's relationship to a conditionStatus.
    // Valid operators are Equal, NotEqual.
    Operator ClusterConditionOperator `json:"operator"`

    // ConditionStatus specifies the ClusterStatue condition status.
    ConditionStatus string `json:"conditionStatus"`
}

// ClusterConditionOperator is the set of operators that can be used in the cluster condition requirement.
type ClusterConditionOperator string

const (
    // ClusterConditionEqual means equal match.
    ClusterConditionEqual ClusterConditionOperator = "Equal"

    // ClusterConditionNotEqual means not equal match.
    ClusterConditionNotEqual ClusterConditionOperator = "NotEqual"
)

// TaintToAdd descries the NoExecute effect taint that need to be applied to the cluster.
type TaintToAdd struct {
    // Key represents the taint key to be applied to a cluster.
    // +required
    Key string `json:"key"`

    // Value represents the taint value corresponding to the taint key.
    // +optional
    Value string `json:"value,omitempty"`

    // AddAfterSeconds describes the wait seconds for the taint to be added after
    // any one DecisionControl matches, which is calculated from the
    // LastTransitionTime of the matching Condition.
    // +optional
    AddAfterSeconds *int64 `json:"addAfterSeconds,omitempty"`
}
```

#### YAML 举例

让我们举一个例子来解释污点处理模块的处理逻辑，用户创建如下 TaintDetector 资源：

```yaml
apiVersion: "policy.karmada.io/v1alpha1"
kind: "TaintDetector"
metadata:
  name: "detect-cluster-not-ready"
spec:
  decisionMatches:
  - clusterConditionMatch:
    conditionType: "Ready"
    operator: Equal
    conditionStatus: "False"
  clusterAffinity:
    clusterNames:
    - "member1"
    - "member2"
  taintsToAdd:
  - key: "cluster.karmada.io/unreachable"
    addAfterSeconds: 300
```

当集群 member1 的状态变更为：

```yaml
apiVersion: cluster.karmada.io/v1alpha1
kind: Cluster
metadata:
  name: member1
spec:
  apiEndpoint: https://172.18.0.4:6443
status:
  conditions:
  - lastTransitionTime: "2025-01-17T02:36:26Z"
    message: "cluster is not reachable"
    reason: ClusterNotReachable
    status: False
    type: Ready
```

Karmada 系统能够监听到该状态变化，匹配到 `detect-cluster-not-ready` TaintDetector 策略，在5分钟后，为集群 member1 添加相应的污点：

```yaml
apiVersion: cluster.karmada.io/v1alpha1
kind: Cluster
metadata:
  name: member1
spec:
  apiEndpoint: https://172.18.0.4:6443
  taint:
  - effect: NoExecute
    key: cluster.karmada.io/not-ready
    timeAdded: "2025-01-17T02:41:26Z"
```

至此，集群 member1 被打上了 key 为 `cluster.karmada.io/not-ready` 的 NoExecute 污点，这将为 Failover 执行阶段提供提供输入。

#### 集群 unjoin 时添加 terminating 污点行为合理性

`cluster-controller` 控制器当发现集群处于删除中时，会为其添加一个 key 为 `cluster.karmada.io/terminating` 的 NoExecute 污点，如果用户没有设置相关的容忍，会触发该集群上的资源进行迁移。这种行为是否合理，我们是否应该建议用户主动进行业务规划，再 unjoin 集群之前，已经明确并完成集群上资源的迁移或是保留。

Karmada 系统提供了集群上资源保留的策略 https://karmada.io/docs/next/tutorials/resource-migration/#step-5-set-preserveresourcesondeletion-for-propagationpolicy。

### 执行阶段

执行阶段会对具有 NoExecute 污点集群上的每个资源展开分析。首先会检测资源是否具有驱逐资格，如果经过检测发现该资源不具有驱逐资格，将退出执行流程。接下来，会依次经过预迁移、迁移、驱逐阶段的处理，从而完成整体的 Failover 执行行为。

#### 驱逐资格检查

我们计划引入资源粒度的集群故障迁移，通过在 PropagationPolicy/ClusterPropagationPolicy 中扩展字段来实现：

```go
// FailoverBehavior indicates failover behaviors in case of an application or
// cluster failure.
type FailoverBehavior struct {
  // Application indicates failover behaviors in case of application failure.
  // If this value is nil, failover is disabled.
  // If set, the PropagateDeps should be true so that the dependencies could
  // be migrated along with the application.
  // +optional
  Application *ApplicationFailoverBehavior `json:"application,omitempty"`

  // Cluster indicates failover behaviors in case of cluster failure.
  // If this value is nil, failover is disabled.
  // +optional
  Cluster *ClusterFailoverBehavior `json:"cluster,omitempty"`
}

// ClusterFailoverBehavior indicates cluster failover behaviors.
type ClusterFailoverBehavior struct {
  // PurgeMode represents how to deal with the legacy applications on the
  // cluster from which the application is migrated.
  // Valid options are "Immediately", "Graciously" and "Never".
  // Defaults to "Graciously".
  // +kubebuilder:validation:Enum=Immediately;Graciously;Never
  // +kubebuilder:default=Graciously
  // +optional
  PurgeMode PurgeMode `json:"purgeMode,omitempty"`

  // TolerationSeconds represents the period of time Karmada should wait
  // after reaching the desired state before performing failover process.
  // If not specified, Karmada will immediately perform failover process.
  // Defaults to 300s.
  // +kubebuilder:default=300
  // +optional
  TolerationSeconds *int32 `json:"tolerationSeconds,omitempty"`
}
```

只有当用户设置了集群故障迁移 ClusterFailoverBehavior 字段，该 PropagationPolicy/ClusterPropagationPolicy 命中的资源才有可能在集群故障之后触发迁移，否则不会触发资源的迁移。

> 待讨论点：Failover 特性是否需要覆盖 taint-manager 的功能，如果 Failover 关闭，taint-manager 的功能是否也关闭了？下述检查项按照覆盖情况进行分析。

此外，我们可以将 GracefulEviction FeatureGate 废弃掉，因为关于优雅驱逐的行为，可以由 `PurgeMode` 字段进行控制。

让我们来逐一罗列下具体的检测项：

1、判断 Failover FeatureGate打开，如果打开则通过，否则过滤；
2、根据 ClusterFailoverBehavior 字段的内容来判断目标资源是否存在迁移的需求，如果 ClusterFailoverBehavior 字段为 nil，则表示目标资源不需要迁移；
3、计算资源的分发容忍策略是否能够容忍集群上的所有 NoExecute 污点；
4、根据 ClusterAffinity 初步判断是否有可选的迁移目标集群，具体检查标准为：
- 分发策略为 Duplicate 类型：如果用户有设置 SpreadConstraint，其 Max 值小于满足 ClusterAffinity 的集群数，则通过，否则过滤。
- 分发策略为 Divided 类型：在排除之前的调度结果外，仍存在匹配 ClusterAffinity 的集群。

ClusterFailoverBehavior 结构体中的 TolerationSeconds 字段用于表示污点被添加之后的容忍时间，其与 PropagationPolicy/ClusterPropagationPolicy 资源中的 `.spec.placement.clusterTolerations[*].tolerationSeconds` 字段是共同生效的，按照它们中的最长容忍时间来处理。同时因为这一点，系统也无需再通过 karmada-webhook 组件为 PropagationPolicy/ClusterPropagationPolicy 资源添加默认的容忍了。

#### 预迁移

预迁移的目的是为了借助调度器进行一次真实的资源调度，来确定是否存在候选集群可供资源迁移，如果调度成功的话，这些集群也将成为最终的迁移目的集群。

预迁移由 `taint-manger` 负责处理，该控制器的输入是集群被添加了 NoExecute 污点，输出是污点所属集群从调度结果中移除，并移入驱逐队列中。

由于以上操作会修改 binding 对象的 `.spec.clusters` 和 `.spec.gracefulEvictionTasks` 字段，按照与调度器的约定（后续需要单独整理控制器与调度器之间的所有约定，即厘清两者之间的边界），这会触发调度器调度。

> 待讨论点：`taint-manager` 命名是否合理，从命名上给人的感觉是对污点进行管理，而不是根据污点进行后续处理。

> 待讨论点：`GracefulEvictionTask` 命名中的 Graceful 有一定迷惑性。

当前 `taint-manager` 控制器并不是按照单独一个控制器的形式同其他控制器一同启动的，而是同 `cluster-controller` 控制器一同启动，这次迭代，我们可以将其优化为一个普通的控制器，用户可以通过控制器管控实现统一控制。

#### 迁移

当调度完成之后，binding-controller 控制器会进行 reconcile，将资源按照最新的调度结果分发。分发过程中不会移除存在于驱逐队列 GracefulEvictionTasks 中的集群，从而保证了 binding-controller 控制器不会做驱逐操作。

该部分逻辑已经满足要求，不需要进行修改。

#### 驱逐

经过**预迁移**处理之后，资源的调度结果中待驱逐的集群已经处于驱逐队列 GracefulEvictionTasks 中，graceful_eviction_controller 控制器会负责驱逐操作，但前提是需要等调度器完成调度，这一判断条件为：`binding.Status.schedulerObservedGeneration == binding.Generation`。

关于如何定义调度成功，当前 karmada-scheduler 组件的逻辑为，只有当调度流程完成并且调度结果不为空，才能视为调度成功；如果调度完成但调度结果为空，不会被标记为调度成功，调度器也不会进行重试；其他的出错情况都会进行调度重试。

但这也是 https://github.com/karmada-io/karmada/issues/3747 问题出现的根本原因，由于调度器调度之后的结果为空，无法满足 `binding.Status.schedulerObservedGeneration == binding.Generation` 判断条件，导致无法进入 graceful_eviction_controller 控制器的驱逐流程，即使集群恢复之后，当前资源也无法重新调度至该集群，从而形成逻辑上的死锁。

为了解决这个问题，打破死锁，可以考虑采用以下三种策略：
1、不再区分调度完成与调度成功，即使调度结果为空，也设置 `binding.Status.schedulerObservedGeneration = binding.Generation`。但这不能排除出现一直调度失败的情况，例如，对于拆分类型的负载分发，集群资源不足导致的调度失败。
2、控制器的 `graceful-eviction-timeout` 参数用来表示驱逐队列中任务清理的最长等待时间，这个时间是从调度完成开始计算的，需要将其修改为按照任务加入队列的时间开始计算。
3、在调度过程中允许将资源调度至存在于驱逐队列中的集群，如果某个集群同时存在于调度结果与驱逐队列中，由 graceful_eviction_controller 控制器负责将驱逐队列中的任务清空。（这点可以通过新增一个下文提到的插件来实现，该插件判断某个集群是否同时存在与调度结果与驱逐队列，如果是的话，通过。）

> 待讨论点：还有另外一种想法是将控制器的 `graceful-eviction-timeout` 参数废弃掉，不存在驱逐队列任务清理最长等待时间。

接下来，让我们来看下何时对驱逐队列中的任务进行驱逐。

系统内置一系列插件，用于判断驱逐队列中的每个任务是否能够被清理。这一系列插件可以通过控制器参数的形式，提供给用户来控制开启指定插件，当前阶段仅内置一个 `ResourceHealth` 的插件，后续可以迭代更多的插件，例如当启用了 MCI 特性时，检查负载后端的 EndpointSlice 资源是否被清理。

`ResourceHealth` 插件通过检查资源在 binding 中的聚合状态中的 `Health` 字段进行判断，如果值为 `Healthy`，则插件通过，否则不通过。

只有当所有启用的插件均通过之后，才能驱逐目标任务。

### Test Plan

TODO

## Alternatives
