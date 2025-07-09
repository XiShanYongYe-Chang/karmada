/*
Copyright 2022 The Karmada Authors.

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

package gracefuleviction

import (
	"time"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	"github.com/karmada-io/karmada/pkg/controllers/gracefuleviction/config"
	"github.com/karmada-io/karmada/pkg/sharedcli/ratelimiterflag"
	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/fedinformer/genericmanager"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// DynamicRateLimiter implements a rate limiter that dynamically adjusts its rate
// based on the overall health of the clusters managed by Karmada.
type DynamicRateLimiter[T comparable] struct {
	resourceEvictionRate          float32
	secondaryResourceEvictionRate float32
	unhealthyClusterThreshold     float32
	largeClusterNumThreshold      int
	informerManager               genericmanager.SingleClusterInformerManager
}

// NewDynamicRateLimiter creates a new DynamicRateLimiter with the given options.
func NewDynamicRateLimiter[T comparable](informerManager genericmanager.SingleClusterInformerManager, opts config.GracefulEvictionOptions) workqueue.TypedRateLimiter[T] {
	return &DynamicRateLimiter[T]{
		resourceEvictionRate:          opts.ResourceEvictionRate,
		secondaryResourceEvictionRate: opts.SecondaryResourceEvictionRate,
		unhealthyClusterThreshold:     opts.UnhealthyClusterThreshold,
		largeClusterNumThreshold:      opts.LargeClusterNumThreshold,
		informerManager:               informerManager,
	}
}

// When determines how long to wait before processing an item.
func (d *DynamicRateLimiter[T]) When(item T) time.Duration {
	currentRate := d.getCurrentRate()
	if currentRate == 0 {
		return 1000 * time.Second
	}
	return time.Duration(1 / currentRate * float32(time.Second))
}

// getCurrentRate returns the current rate based on cluster health status
func (d *DynamicRateLimiter[T]) getCurrentRate() float32 {
	clusterGVR := clusterv1alpha1.SchemeGroupVersion.WithResource("clusters")

	var lister = d.informerManager.Lister(clusterGVR)
	if lister == nil {
		klog.Errorf("Failed to get cluster lister, falling back to secondary rate")
		return d.secondaryResourceEvictionRate
	}

	clusters, err := lister.List(labels.Everything())
	if err != nil {
		klog.Errorf("Failed to list clusters from informer cache: %v, falling back to secondary rate", err)
		return d.secondaryResourceEvictionRate
	}

	totalClusters := len(clusters)
	if totalClusters == 0 {
		return d.resourceEvictionRate
	}

	unhealthyClusters := 0
	for _, clusterObj := range clusters {
		cluster, ok := clusterObj.(*clusterv1alpha1.Cluster)
		if !ok {
			continue
		}
		if !util.IsClusterReady(&cluster.Status) {
			unhealthyClusters++
		}
	}

	failureRate := float32(unhealthyClusters) / float32(totalClusters)
	isUnhealthy := failureRate > d.unhealthyClusterThreshold
	if !isUnhealthy {
		return d.resourceEvictionRate
	}

	isLargeScale := totalClusters > d.largeClusterNumThreshold
	if isLargeScale {
		klog.V(2).Infof("System is unhealthy (failure rate: %.2f), downgrading eviction rate to secondary rate: %.2f/s",
			failureRate, d.secondaryResourceEvictionRate)
		return d.secondaryResourceEvictionRate
	}

	klog.V(2).Infof("System is unhealthy (failure rate: %.2f) and instance is small, halting eviction.", failureRate)
	return 0
}

// Forget is called when an item is successfully processed.
func (d *DynamicRateLimiter[T]) Forget(item T) {
	// No-op
}

// NumRequeues returns the number of times an item has been requeued.
func (d *DynamicRateLimiter[T]) NumRequeues(item T) int {
	return 0
}

// NewGracefulEvictionRateLimiter creates a rate limiter for graceful eviction controllers
// It combines the dynamic rate limiter with the default controller rate limiter
func NewGracefulEvictionRateLimiter[T comparable](
	informerManager genericmanager.SingleClusterInformerManager,
	evictionOpts config.GracefulEvictionOptions,
	rateLimiterOpts ratelimiterflag.Options) workqueue.TypedRateLimiter[T] {

	dynamicLimiter := NewDynamicRateLimiter[T](informerManager, evictionOpts)
	defaultLimiter := ratelimiterflag.DefaultControllerRateLimiter[T](rateLimiterOpts)
	return workqueue.NewTypedMaxOfRateLimiter[T](dynamicLimiter, defaultLimiter)
}
