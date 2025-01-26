package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
