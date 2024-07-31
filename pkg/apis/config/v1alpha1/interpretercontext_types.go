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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ResourceInterpreterContext describes an interpreter context request and response.
type ResourceInterpreterContext struct {
	metav1.TypeMeta `json:",inline"`

	// Request describes the attributes for the interpreter request.
	// +optional
	Request *ResourceInterpreterRequest `json:"request,omitempty"`

	// Response describes the attributes for the interpreter response.
	// +optional
	Response *ResourceInterpreterResponse `json:"response,omitempty"`
}

// ResourceInterpreterRequest describes the interpreter.Attributes for the interpreter request.
type ResourceInterpreterRequest struct {
	// UID is an identifier for the individual request/response.
	// The UID is meant to track the round trip (request/response) between the karmada and the WebHook, not the user request.
	// It is suitable for correlating log entries between the webhook and karmada, for either auditing or debugging.
	// +required
	UID types.UID `json:"uid"`

	// Kind is the fully-qualified type of object being submitted (for example, v1.Pod or autoscaling.v1.Scale)
	// +required
	Kind metav1.GroupVersionKind `json:"kind"`

	// Name is the name of the object as presented in the request.
	// +required
	Name string `json:"name"`

	// Namespace is the namespace associated with the request (if any).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Operation is the operation being performed.
	// +required
	Operation InterpreterOperation `json:"operation"`

	// Object is the object from the incoming request.
	// +optional
	Object runtime.RawExtension `json:"object,omitempty"`

	// ObservedObject is the object observed from the kube-apiserver of member clusters.
	// Not nil only when InterpreterOperation is InterpreterOperationRetain.
	// +optional
	ObservedObject *runtime.RawExtension `json:"observedObject,omitempty"`

	// DesiredReplicas represents the desired pods number which webhook should revise with.
	// It'll be set only if InterpreterOperation is InterpreterOperationReviseReplica.
	// +optional
	DesiredReplicas *int32 `json:"replicas,omitempty"`

	// AggregatedStatus represents status list of the resource running in each member cluster.
	// It'll be set only if InterpreterOperation is InterpreterOperationAggregateStatus.
	// +optional
	AggregatedStatus []workv1alpha2.AggregatedStatusItem `json:"aggregatedStatus,omitempty"`
}

// ResourceInterpreterResponse describes an interpreter response.
type ResourceInterpreterResponse struct {
	// UID is an identifier for the individual request/response.
	// This must be copied over from the corresponding ResourceInterpreterRequest.
	// +required
	UID types.UID `json:"uid"`

	// Successful indicates whether the request be processed successfully.
	// +required
	Successful bool `json:"successful"`

	// Status contains extra details information about why the request not successful.
	// This filed is not consulted in any way if "Successful" is "true".
	// +optional
	Status *RequestStatus `json:"status,omitempty"`

	// The patch body. We only support "JSONPatch" currently which implements RFC 6902.
	// +optional
	Patch []byte `json:"patch,omitempty"`

	// The type of Patch. We only allow "JSONPatch" currently.
	// +optional
	PatchType *PatchType `json:"patchType,omitempty" protobuf:"bytes,5,opt,name=patchType"`

	// ReplicaRequirements represents the requirements required by each replica.
	// Required if InterpreterOperation is InterpreterOperationInterpretReplica.
	// +optional
	ReplicaRequirements *workv1alpha2.ReplicaRequirements `json:"replicaRequirements,omitempty"`

	// Replicas represents the number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified.
	// Required if InterpreterOperation is InterpreterOperationInterpretReplica.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Dependencies represents the reference of dependencies object.
	// Required if InterpreterOperation is InterpreterOperationInterpretDependency.
	// +optional
	Dependencies []DependentObjectReference `json:"dependencies,omitempty"`

	// RawStatus represents the referencing object's status.
	// +optional
	RawStatus *runtime.RawExtension `json:"rawStatus,omitempty"`

	// Healthy represents the referencing object's healthy status.
	// +optional
	Healthy *bool `json:"healthy,omitempty"`
}

// RequestStatus holds the status of a request.
type RequestStatus struct {
	// Message is human-readable description of the status of this operation.
	// +optional
	Message string `json:"message,omitempty"`

	// Code is the HTTP return code of this status.
	// +optional
	Code int32 `json:"code,omitempty"`
}

// PatchType is the type of patch being used to represent the mutated object
type PatchType string

const (
	// PatchTypeJSONPatch represents the JSONType.
	PatchTypeJSONPatch PatchType = "JSONPatch"
)

// DependentObjectReference contains enough information to locate the referenced object inside current cluster.
type DependentObjectReference struct {
	// APIVersion represents the API version of the referent.
	// +required
	APIVersion string `json:"apiVersion"`

	// Kind represents the Kind of the referent.
	// +required
	Kind string `json:"kind"`

	// Namespace represents the namespace for the referent.
	// For non-namespace scoped resources(e.g. 'ClusterRole')，do not need specify Namespace,
	// and for namespace scoped resources, Namespace is required.
	// If Namespace is not specified, means the resource is non-namespace scoped.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name represents the name of the referent.
	// Name and LabelSelector cannot be empty at the same time.
	// +optional
	Name string `json:"name,omitempty"`

	// LabelSelector represents a label query over a set of resources.
	// If name is not empty, labelSelector will be ignored.
	// Name and LabelSelector cannot be empty at the same time.
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

type RollingStrategy struct {
	// Paused indicates that the rolling update should be paused.
	Paused *bool `json:"paused,omitempty"`

	// Partition is the desired number or percent of Pods in old revisions.
	// For example:
	// - Replicas=5 and Partition=2 means that the controller will keep 2
	//   Pods in old revisions and 3 Pods in new revisions.
	//
	// Refer to https://openkruise.io/docs/user-manuals/cloneset#partition
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// MaxUnavailable is an optional field that specifies the maximum number of
	// Pods that can be unavailable during the update process.
	// Refer to https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#max-unavailable
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// MaxSurge is an optional field that specifies the maximum number of Pods
	// that can be created over the desired number of Pods.
	// Refer to https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#max-surge
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`
}

type UnifiedRollingStatus struct {
	// Generation is a sequence number representing a specific generation of the desired state. Set by the system and monotonically increasing, per-resource. May be compared, such as for RAW and WAW consistency.
	// Refer to https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#metadata
	Generation *int64 `json:"generation,omitempty"`

	// ObservedGeneration represent the most recent generation observed by the daemon set controller.
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`

	// ResourceTemplateGeneration represent the generation of the resource template of karmada resource.
	ResourceTemplateGeneration *int64 `json:"resourceTemplateGeneration,omitempty"`

	// Replicas is the number of Pods created by the controller.
	Replicas *int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of Pods created by the controller that have a Ready Condition.
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of Pods created by the controller that have a Ready Condition for at least minReadySeconds.
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`

	// UpdatedReplicas is the number of Pods created by the controller from the ReplicaSet version indicated by updateRevision.
	UpdatedReplicas *int32 `json:"updatedReplicas,omitempty"`

	// UpdatedReadyReplicas is the number of Pods created by the controller from the ReplicaSet version indicated by updateRevision and have a Ready Condition.
	UpdatedReadyReplicas *int32 `json:"updatedReadyReplicas,omitempty"`
}
