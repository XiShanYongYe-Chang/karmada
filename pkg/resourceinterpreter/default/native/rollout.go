package native

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/rollout"
)

func cloneSetRollingStrategy(object *unstructured.Unstructured) (*configv1alpha1.RollingStrategy, error) {
	var maxSurge *intstr.IntOrString
	var partition *intstr.IntOrString
	var maxUnavailable *intstr.IntOrString
	m, found, err := unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "partition")
	if err == nil && found {
		partition = unmarshalIntStr(m)
	} else {
		partition = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	m, found, err = unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "maxUnavailable")
	if err == nil && found {
		maxUnavailable = unmarshalIntStr(m)
	} else {
		maxUnavailable = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	m, found, err = unstructured.NestedFieldCopy(object.Object, "spec", "updateStrategy", "maxSurge")
	if err == nil && found {
		maxSurge = unmarshalIntStr(m)
	} else {
		maxSurge = &intstr.IntOrString{Type: intstr.Int, IntVal: 0}
	}

	return &configv1alpha1.RollingStrategy{
		MaxSurge:       maxSurge,
		Partition:      partition,
		MaxUnavailable: maxUnavailable,
	}, nil
}

func cloneSetReviseRollingStrategy(object *unstructured.Unstructured, rollingStrategy *configv1alpha1.RollingStrategy) (*unstructured.Unstructured, error) {
	var err error
	if rollingStrategy.Partition == nil {
		err = unstructured.SetNestedField(object.Object, int64(0), "spec", "updateStrategy", "partition")
	} else if rollingStrategy.Partition.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.Partition.IntVal), "spec", "updateStrategy", "partition")
	} else if rollingStrategy.Partition.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.Partition.StrVal, "spec", "updateStrategy", "partition")
	}
	if err != nil {
		return nil, err
	}

	if rollingStrategy.MaxUnavailable == nil {
		unstructured.RemoveNestedField(object.Object, "spec", "updateStrategy", "maxUnavailable")
	} else if rollingStrategy.MaxUnavailable.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.MaxUnavailable.IntVal), "spec", "updateStrategy", "maxUnavailable")
	} else if rollingStrategy.MaxUnavailable.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.MaxUnavailable.StrVal, "spec", "updateStrategy", "maxUnavailable")
	}
	if err != nil {
		return nil, err
	}

	if rollingStrategy.MaxSurge == nil {
		unstructured.RemoveNestedField(object.Object, "spec", "updateStrategy", "maxSurge")
	} else if rollingStrategy.MaxSurge.Type == intstr.Int {
		err = unstructured.SetNestedField(object.Object, int64(rollingStrategy.MaxSurge.IntVal), "spec", "updateStrategy", "maxSurge")
	} else if rollingStrategy.MaxSurge.Type == intstr.String {
		err = unstructured.SetNestedField(object.Object, rollingStrategy.MaxSurge.StrVal, "spec", "updateStrategy", "maxSurge")
	}
	if err != nil {
		return nil, err
	}

	return object, nil
}

func cloneSetRollingStatus(_ *unstructured.Unstructured, rawStatus *runtime.RawExtension) (*configv1alpha1.UnifiedRollingStatus, error) {
	cloneSetStatus := struct {
		// ObservedGeneration is the most recent generation observed for this CloneSet. It corresponds to the
		// CloneSet's generation, which is updated on mutation by the API Server.
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// Replicas is the number of Pods created by the CloneSet controller.
		Replicas int32 `json:"replicas"`

		// ReadyReplicas is the number of Pods created by the CloneSet controller that have a Ready Condition.
		ReadyReplicas int32 `json:"readyReplicas"`

		// AvailableReplicas is the number of Pods created by the CloneSet controller that have a Ready Condition for at least minReadySeconds.
		AvailableReplicas int32 `json:"availableReplicas"`

		// UpdatedReplicas is the number of Pods created by the CloneSet controller from the CloneSet version
		// indicated by updateRevision.
		UpdatedReplicas int32 `json:"updatedReplicas"`

		// UpdatedReadyReplicas is the number of Pods created by the CloneSet controller from the CloneSet version
		// indicated by updateRevision and have a Ready Condition.
		UpdatedReadyReplicas int32 `json:"updatedReadyReplicas"`

		// ExpectedUpdatedReplicas is the number of Pods that should be updated by CloneSet controller.
		// This field is calculated via Replicas - Partition.
		ExpectedUpdatedReplicas int32 `json:"expectedUpdatedReplicas,omitempty"`

		// UpdateRevision, if not empty, indicates the latest revision of the CloneSet.
		UpdateRevision string `json:"updateRevision,omitempty"`

		// currentRevision, if not empty, indicates the current revision version of the CloneSet.
		CurrentRevision string `json:"currentRevision,omitempty"`

		// LabelSelector is label selectors for query over pods that should match the replica count used by HPA.
		LabelSelector string `json:"labelSelector,omitempty"`

		// Generation is the generation of the CloneSet.
		Generation int64 `json:"generation,omitempty"`

		// KarmadaResourceGeneration is the generation of the KarmadaResource.
		KarmadaResourceGeneration int64 `json:"karmadaResourceGeneration,omitempty"`
	}{}

	if err := json.Unmarshal(rawStatus.Raw, &cloneSetStatus); err != nil {
		return nil, err
	}

	return &configv1alpha1.UnifiedRollingStatus{
		Generation:                 pointer.Int64(cloneSetStatus.Generation),
		Replicas:                   pointer.Int32(cloneSetStatus.Replicas),
		ReadyReplicas:              pointer.Int32(cloneSetStatus.ReadyReplicas),
		UpdatedReplicas:            pointer.Int32(cloneSetStatus.UpdatedReplicas),
		AvailableReplicas:          pointer.Int32(cloneSetStatus.AvailableReplicas),
		UpdatedReadyReplicas:       pointer.Int32(cloneSetStatus.UpdatedReadyReplicas),
		ObservedGeneration:         pointer.Int64(cloneSetStatus.ObservedGeneration),
		ResourceTemplateGeneration: pointer.Int64(cloneSetStatus.KarmadaResourceGeneration),
	}, nil
}

// retentionInterpreter is the function that retains values from "observed" object.
type rollingStrategyInterpreter func(object *unstructured.Unstructured) (*configv1alpha1.RollingStrategy, error)
type reviseRollingStrategyInterpreter func(object *unstructured.Unstructured, rollingStrategy *configv1alpha1.RollingStrategy) (*unstructured.Unstructured, error)

type rollingStatusInterpreter func(object *unstructured.Unstructured, rawStatus *runtime.RawExtension) (*configv1alpha1.UnifiedRollingStatus, error)

func getAllDefaultRollingStrategyInterpreter() map[schema.GroupVersionKind]rollingStrategyInterpreter {
	s := make(map[schema.GroupVersionKind]rollingStrategyInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetRollingStrategy
	return s
}

func reviseAllDefaultRollingStrategyInterpreter() map[schema.GroupVersionKind]reviseRollingStrategyInterpreter {
	s := make(map[schema.GroupVersionKind]reviseRollingStrategyInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetReviseRollingStrategy
	return s
}

func getAllDefaultRollingStatusInterpreter() map[schema.GroupVersionKind]rollingStatusInterpreter {
	s := make(map[schema.GroupVersionKind]rollingStatusInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetRollingStatus
	return s
}

// unmarshalIntStr return *intstr.IntOrString
func unmarshalIntStr(m interface{}) *intstr.IntOrString {
	field := &intstr.IntOrString{}
	data, _ := json.Marshal(m)
	_ = json.Unmarshal(data, field)
	return field
}
