package native

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/rollout"
)

type rollingStatusInterpreter func(object *unstructured.Unstructured, rawStatus *runtime.RawExtension) (*configv1alpha1.UnifiedRollingStatus, error)

func getAllDefaultRollingStatusInterpreter() map[schema.GroupVersionKind]rollingStatusInterpreter {
	s := make(map[schema.GroupVersionKind]rollingStatusInterpreter)
	s[rollout.CloneSetGroupVersionKind] = cloneSetRollingStatus
	return s
}

func cloneSetRollingStatus(_ *unstructured.Unstructured, rawStatus *runtime.RawExtension) (*configv1alpha1.UnifiedRollingStatus, error) {
	// 放在default中进行维护，增加了维护成本，是否可以改为通过字段路径进行获取
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
		// 成员集群中资源的该字段是谁设置的呢？
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
