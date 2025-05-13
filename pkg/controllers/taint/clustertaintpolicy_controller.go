/*
Copyright 2025 The Karmada Authors.

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

package taint

import (
	"context"
	"reflect"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/pkg/sharedcli/ratelimiterflag"
	"github.com/karmada-io/karmada/pkg/util"
)

// ControllerName is the controller name that will be used when reporting events.
const ControllerName = "clustertaintpolicy-controller"

// ClusterTaintPolicyController is to sync ClusterTaintPolicy.
type ClusterTaintPolicyController struct {
	client.Client
	EventRecorder      record.EventRecorder
	RateLimiterOptions ratelimiterflag.Options
}

// Reconcile performs a full reconciliation for the object referred to by the Request.
// The Controller will requeue the Request to be processed again if an error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *ClusterTaintPolicyController) Reconcile(ctx context.Context, req controllerruntime.Request) (controllerruntime.Result, error) {
	klog.V(4).Infof("Reconciling Cluster %s.", req.Name)

	clusterObj := &clusterv1alpha1.Cluster{}
	if err := c.Client.Get(ctx, req.NamespacedName, clusterObj); err != nil {
		klog.Errorf("Failed to get Cluster(%s), error: %v", req.Name, err)
		return controllerruntime.Result{}, client.IgnoreNotFound(err)
	}

	clusterTaintPolicyList := &policyv1alpha1.ClusterTaintPolicyList{}
	listOption := &client.ListOptions{UnsafeDisableDeepCopy: ptr.To(true)}
	if err := c.Client.List(ctx, clusterTaintPolicyList, listOption); err != nil {
		klog.Errorf("Failed to list ClusterTaintPolicy, error: %v", err)
		return controllerruntime.Result{}, err
	}

	clusterCopyObj := clusterObj.DeepCopy()
	var duration time.Duration
	for _, policy := range clusterTaintPolicyList.Items {
		if policy.Spec.TargetClusters != nil && !util.ClusterMatches(clusterCopyObj, *policy.Spec.TargetClusters) {
			continue
		}

		singleDuration := taintClusterWithPolicy(clusterCopyObj, policy)
		if singleDuration < duration {
			duration = singleDuration
		}
	}

	if !reflect.DeepEqual(clusterObj.Spec.Taints, clusterCopyObj.Spec.Taints) {
		objPatch := client.MergeFrom(clusterObj)
		err := c.Client.Patch(ctx, clusterCopyObj, objPatch)
		if err != nil {
			klog.Errorf("Failed to patch Cluster(%s), error: %v", req.Name, err)
			return controllerruntime.Result{}, err
		}
	}
	return controllerruntime.Result{RequeueAfter: duration}, nil
}

func taintClusterWithPolicy(cluster *clusterv1alpha1.Cluster, policy policyv1alpha1.ClusterTaintPolicy) time.Duration {
	matches := conditionMatches(cluster, policy.Spec.MatchConditions)

	var changeDuration time.Duration
	switch matches {
	case true:
		changeDuration = addOnMatchWithDuration(cluster, policy.Spec.MatchConditions, *policy.Spec.AddOnMatchSeconds)
	case false:
		changeDuration = removeOnMismatchWithDuration(cluster, policy.Spec.MatchConditions, *policy.Spec.RemoveOnMismatchSeconds)
	}

	if changeDuration > 0 {
		klog.V(4).Infof("Cluster(%s) taint will be changed after %f seconds", cluster.Name, changeDuration.Seconds())
		return changeDuration
	}

	if matches {
		addTaintsOnCluster(cluster, policy.Spec.Taints, metav1.Now())
	} else {
		removeTaintsOnCluster(cluster, policy.Spec.Taints)
	}
	return 0
}

func conditionMatches(cluster *clusterv1alpha1.Cluster, matchConditions []policyv1alpha1.MatchCondition) bool {
	matches := true
	for _, matchCondition := range matchConditions {
		match := false
		for _, clusterCondition := range cluster.Status.Conditions {
			if clusterCondition.Type != matchCondition.ConditionType {
				continue
			}

			switch matchCondition.Operator {
			case policyv1alpha1.MatchConditionOpIn:
				for _, value := range matchCondition.StatusValues {
					if clusterCondition.Status == value {
						match = true
						break
					}
				}
			case policyv1alpha1.MatchConditionOpNotIn:
				match = true
				for _, value := range matchCondition.StatusValues {
					if clusterCondition.Status == value {
						match = false
						break
					}
				}
			default:
				klog.Errorf("Unsupported MatchCondition operator: %s", matchCondition.Operator)
				return false
			}
		}

		if !match {
			matches = false
			break
		}
	}
	return matches
}

func addOnMatchWithDuration(cluster *clusterv1alpha1.Cluster, matchConditions []policyv1alpha1.MatchCondition, durationSeconds int32) time.Duration {
	var retryDuration time.Duration
	for _, matchCondition := range matchConditions {
		for _, clusterCondition := range cluster.Status.Conditions {
			if clusterCondition.Type != matchCondition.ConditionType {
				continue
			}

			tmpDuration := time.Until(clusterCondition.LastTransitionTime.Add(time.Duration(durationSeconds) * time.Second))
			if tmpDuration > retryDuration {
				retryDuration = tmpDuration
			}
		}
	}
	return retryDuration
}

func removeOnMismatchWithDuration(cluster *clusterv1alpha1.Cluster, matchConditions []policyv1alpha1.MatchCondition, durationSeconds int32) time.Duration {
	retryDuration := time.Duration(durationSeconds) * time.Second
	for _, matchCondition := range matchConditions {
		for _, clusterCondition := range cluster.Status.Conditions {
			if clusterCondition.Type != matchCondition.ConditionType {
				continue
			}

			match := false
			switch matchCondition.Operator {
			case policyv1alpha1.MatchConditionOpIn:
				for _, value := range matchCondition.StatusValues {
					if clusterCondition.Status == value {
						match = true
						break
					}
				}
			case policyv1alpha1.MatchConditionOpNotIn:
				match = true
				for _, value := range matchCondition.StatusValues {
					if clusterCondition.Status == value {
						match = false
						break
					}
				}
			}

			// Ignore match condition
			if match {
				continue
			}

			tmpDuration := time.Until(clusterCondition.LastTransitionTime.Add(time.Duration(durationSeconds) * time.Second))
			if tmpDuration < retryDuration {
				retryDuration = tmpDuration
			}
		}
	}
	return retryDuration
}

func addTaintsOnCluster(cluster *clusterv1alpha1.Cluster, taints []policyv1alpha1.Taint, now metav1.Time) {
	clusterTaints := cluster.Spec.Taints
	for _, taint := range taints {
		exist := false
		for _, clusterTaint := range clusterTaints {
			if clusterTaint.Effect == taint.Effect &&
				clusterTaint.Key == taint.Key &&
				clusterTaint.Value == taint.Value {
				exist = true
				break
			}
		}

		if !exist {
			clusterTaints = append(clusterTaints, corev1.Taint{
				Key:       taint.Key,
				Value:     taint.Value,
				Effect:    taint.Effect,
				TimeAdded: &now,
			})
		}
	}

	sort.Slice(clusterTaints, func(i, j int) bool {
		if clusterTaints[i].Effect != clusterTaints[j].Effect {
			return clusterTaints[i].Effect < clusterTaints[j].Effect
		}
		if clusterTaints[i].Key != clusterTaints[j].Key {
			return clusterTaints[i].Key < clusterTaints[j].Key
		}
		return clusterTaints[i].Value < clusterTaints[j].Value
	})

	cluster.Spec.Taints = clusterTaints
}

func removeTaintsOnCluster(cluster *clusterv1alpha1.Cluster, taints []policyv1alpha1.Taint) {
	clusterTaints := cluster.Spec.Taints
	for _, taint := range taints {
		for index, clusterTaint := range clusterTaints {
			if clusterTaint.Effect == taint.Effect &&
				clusterTaint.Key == taint.Key &&
				clusterTaint.Value == taint.Value {
				clusterTaints = append(clusterTaints[:index], clusterTaints[index+1:]...)
				break
			}
		}
	}
	cluster.Spec.Taints = clusterTaints
}

// SetupWithManager creates a controller and register to controller manager.
func (c *ClusterTaintPolicyController) SetupWithManager(mgr controllerruntime.Manager) error {
	clusterStatusConditionPredicateFn := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		UpdateFunc: func(event event.UpdateEvent) bool {
			oldCluster := event.ObjectOld.(*clusterv1alpha1.Cluster)
			newCluster := event.ObjectNew.(*clusterv1alpha1.Cluster)
			return reflect.DeepEqual(oldCluster.Status.Conditions, newCluster.Status.Conditions)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
	}

	clusterTaintPolicyMapFunc := handler.MapFunc(
		func(ctx context.Context, policyObj client.Object) []reconcile.Request {
			clusterList := &clusterv1alpha1.ClusterList{}
			listOption := &client.ListOptions{UnsafeDisableDeepCopy: ptr.To(true)}
			if err := c.Client.List(ctx, clusterList, listOption); err != nil {
				klog.Errorf("Failed to list Cluster, error: %v", err)
				return nil
			}

			policy := policyObj.(*policyv1alpha1.ClusterTaintPolicy)
			var requests []reconcile.Request
			for _, cluster := range clusterList.Items {
				if policy.Spec.TargetClusters == nil ||
					util.ClusterMatches(&cluster, *policy.Spec.TargetClusters) {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: cluster.Name}})
				}
			}
			return requests
		})

	return controllerruntime.NewControllerManagedBy(mgr).
		Named(ControllerName).
		For(&clusterv1alpha1.Cluster{}).
		WithEventFilter(clusterStatusConditionPredicateFn).
		Watches(&policyv1alpha1.ClusterTaintPolicy{}, handler.EnqueueRequestsFromMapFunc(clusterTaintPolicyMapFunc)).
		WithOptions(controller.Options{RateLimiter: ratelimiterflag.DefaultControllerRateLimiter[controllerruntime.Request](c.RateLimiterOptions)}).
		Complete(c)
}
