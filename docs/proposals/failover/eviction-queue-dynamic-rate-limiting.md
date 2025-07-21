---
title: Eviction Queue with Dynamic Rate Limiting
authors:
  - "@wanggang"
reviewers:
approvers:
creation-date: 2024-07-20
---

# Eviction Queue with Dynamic Rate Limiting

## Summary

This proposal introduces an eviction queue with dynamic rate limiting for the Karmada taint manager. The eviction queue enhances the fault migration mechanism by controlling the rate of resource evictions based on the overall health status of clusters. When a significant portion of clusters becomes unhealthy, the system will automatically adjust the eviction rate or pause evictions to prevent cascading failures. The implementation also provides comprehensive metrics for monitoring the eviction process, improving the observability of the system.

## Motivation

In multi-cluster environments, when clusters experience failures, resources need to be evicted and rescheduled to healthy clusters. However, if many clusters fail simultaneously or in quick succession, aggressive eviction and rescheduling can overwhelm the remaining healthy clusters and the control plane, potentially leading to cascading failures. Currently, Karmada's taint manager lacks a mechanism to control the eviction rate based on the overall system health status.

### Goals

- Implement an eviction queue with dynamic rate limiting based on cluster health status
- Provide metrics for monitoring the eviction queue's performance and system health
- Ensure the eviction queue can handle both ResourceBinding and ClusterResourceBinding resources
- Minimize changes to the existing taint manager architecture while adding these capabilities
- Support configuration of eviction rates and health thresholds through command-line flags

### Non-Goals

- Redesigning the entire taint manager architecture
- Implementing advanced scheduling algorithms for evicted resources
- Providing a UI for monitoring the eviction queue
- Supporting custom rate limiting strategies beyond cluster health status

## Proposal

The proposal introduces a dynamic rate-limited eviction queue that adjusts its processing rate based on the overall health status of clusters. When a significant portion of clusters becomes unhealthy, the system will reduce the eviction rate or pause evictions to prevent cascading failures.

### User Stories

#### Story 1: Preventing Cascading Failures During Large-Scale Outages

As a cluster administrator, I want to ensure that when multiple clusters experience failures simultaneously, the system doesn't overwhelm the remaining healthy clusters with too many evictions at once. The dynamic rate limiting should automatically slow down or pause evictions when a significant portion of clusters becomes unhealthy.

#### Story 2: Monitoring Eviction Performance

As a cluster administrator, I want to monitor the performance of the eviction queue, including the number of pending evictions, processing latency, and success/failure rates. This information helps me understand the system's behavior during failure scenarios and tune the configuration accordingly.

#### Story 3: Configuring Eviction Behavior for Different Environments

As a cluster administrator, I want to configure the eviction behavior based on my environment's characteristics. For example, in a production environment with many critical workloads, I might want to be more conservative with evictions, while in a development environment, I might prioritize quick recovery.

### Design Details

The implementation consists of several components:

#### 1. EvictionQueueOptions

A configuration structure that controls the behavior of the eviction queue:

- `ResourceEvictionRate`: The default eviction rate (per second) when the system is healthy
- `SecondaryResourceEvictionRate`: The reduced eviction rate when the system is unhealthy
- `UnhealthyClusterThreshold`: The threshold above which the system is considered unhealthy
- `LargeClusterNumThreshold`: The threshold that determines whether to reduce or pause evictions

These options can be configured through command-line flags.

#### 2. EvictionWorker

An enhanced version of the AsyncWorker that adds dynamic rate limiting and metrics collection:

- Embeds the AsyncWorker interface to maintain compatibility
- Uses a dynamic rate limiter that adjusts based on cluster health
- Collects metrics on queue depth, processing latency, and success/failure rates
- Supports tracking metrics by resource kind

#### 3. DynamicRateLimiter

A rate limiter that adjusts its rate based on cluster health status:

- Monitors the health status of all clusters using the InformerManager
- Calculates the appropriate rate based on the percentage of unhealthy clusters
- Reduces the rate or pauses evictions when the system is unhealthy
- Updates metrics on cluster health status

#### 4. Metrics Collection

A set of metrics for monitoring the eviction queue and cluster health:

- Queue depth metrics
- Resource kind metrics
- Processing latency metrics
- Success/failure metrics
- Cluster health metrics

#### Component Interactions

1. The controller manager initializes the TaintManager with EvictionQueueOptions
2. The TaintManager creates two EvictionWorker instances, one for ResourceBinding and one for ClusterResourceBinding
3. Each EvictionWorker uses a DynamicRateLimiter that monitors cluster health
4. When resources need to be evicted, they are added to the appropriate queue
5. The DynamicRateLimiter adjusts the processing rate based on cluster health
6. Metrics are collected throughout the process and exposed to Prometheus

### Implementation Details

#### New Files

1. **pkg/controllers/cluster/evictionqueue_config/evictionoption.go**
   - Defines the EvictionQueueOptions structure and methods to register command-line flags

2. **pkg/controllers/cluster/eviction_worker.go**
   - Implements the EvictionWorker interface and concrete evictionWorker type
   - Provides methods for adding items to the queue and processing them
   - Integrates with metrics collection

3. **pkg/controllers/cluster/dynamic_rate_limiter.go**
   - Implements the DynamicRateLimiter type
   - Provides methods for calculating the appropriate rate based on cluster health
   - Updates cluster health metrics

#### Modifications to Existing Files

1. **pkg/controllers/cluster/taint_manager.go**
   - Add EvictionQueueOptions and InformerManager fields to NoExecuteTaintManager
   - Modify the Start method to create and start eviction workers
   - Add getResourceKindFromKey method to extract cluster name and resource kind
   - Update syncCluster, syncBindingEviction, and syncClusterBindingEviction methods

2. **pkg/metrics/cluster.go**
   - Add metrics for eviction queue depth, processing latency, and success/failure rates
   - Add metrics for cluster health status
   - Add methods for recording metrics

3. **cmd/controller-manager/app/options/options.go**
   - Add EvictionQueueOptions field to Options structure
   - Register command-line flags for eviction queue options

4. **cmd/controller-manager/app/controllermanager.go**
   - Pass EvictionQueueOptions to TaintManager when creating it
   - Pass EvictionQueueOptions to controller context

5. **pkg/controllers/context/context.go**
   - Add EvictionQueueOptions field to Options structure

### Workflow

1. When a cluster becomes unhealthy, the taint manager identifies resources that need to be evicted
2. These resources are added to the appropriate eviction queue (ResourceBinding or ClusterResourceBinding)
3. The dynamic rate limiter calculates the appropriate processing rate based on cluster health
4. Resources are processed from the queue at the calculated rate
5. Metrics are collected and exposed to Prometheus for monitoring
6. If the cluster health improves or deteriorates, the rate limiter adjusts the processing rate accordingly

## Alternatives Considered

### Alternative 1: Separate Eviction Manager Component

Instead of enhancing the taint manager with an eviction queue, we could implement a separate eviction manager component that handles all evictions. This would provide a cleaner separation of concerns but would require more significant changes to the existing architecture.

### Alternative 2: Single Queue for All Resource Types

Instead of having separate queues for ResourceBinding and ClusterResourceBinding, we could have a single queue for all resource types. This would require more significant changes to the taint manager but could be considered in the future.

## Implementation Plan

1. Implement the EvictionQueueOptions structure and command-line flags
2. Implement the DynamicRateLimiter
3. Implement the EvictionWorker
4. Add metrics collection
5. Modify the TaintManager to use the eviction queue
6. Add tests and documentation
7. Submit the implementation for review

## Open Issues

1. The current design has two separate eviction queues for ResourceBinding and ClusterResourceBinding. While this aligns with the existing taint manager architecture, it might be more efficient to have a single queue for all resource types. This would require more significant changes to the taint manager but could be considered in the future.

2. The dynamic rate limiting currently only considers the percentage of unhealthy clusters. In the future, we might want to consider other factors such as the load on the API server, the number of pending evictions, and the rate of cluster failures. 