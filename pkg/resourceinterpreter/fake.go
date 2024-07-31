package resourceinterpreter

import (
	"context"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter/default/native"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter/default/thirdparty"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeResourceInterpreterImpl struct {
	defaultInterpreter    *native.DefaultInterpreter
	thirdpartyInterpreter *thirdparty.ConfigurableInterpreter
}

func NewFakeResourceInterpreter() ResourceInterpreter {
	return &fakeResourceInterpreterImpl{
		defaultInterpreter:    native.NewDefaultInterpreter(),
		thirdpartyInterpreter: thirdparty.NewConfigurableInterpreter(),
	}
}

func (i *fakeResourceInterpreterImpl) HookEnabled(objGVK schema.GroupVersionKind, operation configv1alpha1.InterpreterOperation) bool {
	return i.defaultInterpreter.HookEnabled(objGVK, operation) || i.thirdpartyInterpreter.HookEnabled(objGVK, operation)
}

func (i *fakeResourceInterpreterImpl) Start(ctx context.Context) (err error) {
	return nil
}

// GetReplicas returns the desired replicas of the object as well as the requirements of each replica.
func (i *fakeResourceInterpreterImpl) GetReplicas(object *unstructured.Unstructured) (replica int32, requires *workv1alpha2.ReplicaRequirements, err error) {
	replica, requires, hookEnabled, err := i.thirdpartyInterpreter.GetReplicas(object)
	if err != nil {
		return
	}
	if hookEnabled {
		return
	}

	replica, requires, err = i.defaultInterpreter.GetReplicas(object)
	return
}

// ReviseReplica revises the replica of the given object.
func (i *fakeResourceInterpreterImpl) ReviseReplica(object *unstructured.Unstructured, replica int64) (*unstructured.Unstructured, error) {
	obj, hookEnabled, err := i.thirdpartyInterpreter.ReviseReplica(object, replica)
	if err != nil {
		return nil, err
	}
	if hookEnabled {
		return obj, nil
	}

	return i.defaultInterpreter.ReviseReplica(object, replica)
}

// Retain returns the objects that based on the "desired" object but with values retained from the "observed" object.
func (i *fakeResourceInterpreterImpl) Retain(desired *unstructured.Unstructured, observed *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	obj, hookEnabled, err := i.thirdpartyInterpreter.Retain(desired, observed)
	if err != nil {
		return nil, err
	}
	if hookEnabled {
		return obj, nil
	}

	return i.defaultInterpreter.Retain(desired, observed)
}

// AggregateStatus returns the objects that based on the 'object' but with status aggregated.
func (i *fakeResourceInterpreterImpl) AggregateStatus(object *unstructured.Unstructured, aggregatedStatusItems []workv1alpha2.AggregatedStatusItem) (*unstructured.Unstructured, error) {
	obj, hookEnabled, err := i.thirdpartyInterpreter.AggregateStatus(object, aggregatedStatusItems)
	if err != nil {
		return nil, err
	}
	if hookEnabled {
		return obj, nil
	}

	return i.defaultInterpreter.AggregateStatus(object, aggregatedStatusItems)
}

// GetDependencies returns the dependent resources of the given object.
func (i *fakeResourceInterpreterImpl) GetDependencies(object *unstructured.Unstructured) (dependencies []configv1alpha1.DependentObjectReference, err error) {
	dependencies, hookEnabled, err := i.thirdpartyInterpreter.GetDependencies(object)
	if err != nil {
		return
	}
	if hookEnabled {
		return
	}

	dependencies, err = i.defaultInterpreter.GetDependencies(object)
	return
}

// ReflectStatus returns the status of the object.
func (i *fakeResourceInterpreterImpl) ReflectStatus(object *unstructured.Unstructured) (status *runtime.RawExtension, err error) {
	status, hookEnabled, err := i.thirdpartyInterpreter.ReflectStatus(object)
	if err != nil {
		return
	}
	if hookEnabled {
		return
	}

	status, err = i.defaultInterpreter.ReflectStatus(object)
	return
}

// InterpretHealth returns the health state of the object.
func (i *fakeResourceInterpreterImpl) InterpretHealth(object *unstructured.Unstructured) (healthy bool, err error) {
	healthy, hookEnabled, err := i.thirdpartyInterpreter.InterpretHealth(object)
	if err != nil {
		return
	}
	if hookEnabled {
		return
	}

	healthy, err = i.defaultInterpreter.InterpretHealth(object)
	return
}

func (i *fakeResourceInterpreterImpl) GetRollingStrategy(object *unstructured.Unstructured) (*configv1alpha1.RollingStrategy, error) {
	if !i.HookEnabled(object.GroupVersionKind(), configv1alpha1.InterpreterOperationRollingStrategy) {
		return nil, nil
	}
	return i.defaultInterpreter.GetRollingStrategy(object)
}

func (i *fakeResourceInterpreterImpl) ReviseRollingStrategy(object *unstructured.Unstructured, rollingStrategy *configv1alpha1.RollingStrategy) (*unstructured.Unstructured, error) {
	if !i.HookEnabled(object.GroupVersionKind(), configv1alpha1.InterpreterOperationReviseRollingStrategy) {
		return nil, nil
	}
	return i.defaultInterpreter.ReviseRollingStrategy(object, rollingStrategy)
}

func (i *fakeResourceInterpreterImpl) InterpretRollingStatus(object *unstructured.Unstructured, rawStatus *runtime.RawExtension) (*configv1alpha1.UnifiedRollingStatus, error) {
	if !i.HookEnabled(object.GroupVersionKind(), configv1alpha1.InterpreterOperationRollingStatus) {
		return nil, nil
	}
	return i.defaultInterpreter.InterpretRollingStatus(object, rawStatus)
}
