/*
Copyright 2024 The Karmada Authors.

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

package helper

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// UpdateStatusOption defines the function to modify the base object for patch generation.
type UpdateStatusOption func(base client.Object)

// UpdateStatus updates the given object's status in the Kubernetes
// cluster. The object's desired state must be reconciled with the existing
// state inside the passed in callback MutateFn.
//
// The MutateFn is called when updating an object's status.
//
// It returns the executed operation and an error.
//
// Note: changes to any sub-resource other than status will be ignored.
// Changes to the status sub-resource will only be applied if the object
// already exist.
func UpdateStatus(ctx context.Context, c client.Client, obj client.Object, f controllerutil.MutateFn, opts ...UpdateStatusOption) (controllerutil.OperationResult, error) {
	key := client.ObjectKeyFromObject(obj)
	if err := c.Get(ctx, key, obj); err != nil {
		return controllerutil.OperationResultNone, err
	}

	// Create a copy of the object from cache to be used as base for patch generation.
	// We use Patch to update status to avoid the conflict error.
	base := obj.DeepCopyObject().(client.Object)

	if err := mutate(f, key, obj); err != nil {
		return controllerutil.OperationResultNone, err
	}

	// Apply options to modify the base object.
	// This is useful when we want to force update some fields even if they are not changed in the cache.
	// For example, if we want to force update the conditions, we can set the conditions in the base object to nil/empty.
	for _, opt := range opts {
		opt(base)
	}

	patchObj := obj.DeepCopyObject().(client.Object)
	// Set the resource version to empty to ensure the patch is applied blindly.
	// This prevents the request from failing due to a conflict error (409 Conflict)
	// when the client's cache is stale (i.e., the resource version in the cache
	// does not match the one in the API server).
	patchObj.SetResourceVersion("")

	if equality.Semantic.DeepEqual(base, patchObj) {
		return controllerutil.OperationResultNone, nil
	}

	patch := client.MergeFrom(base)

	if err := c.Status().Patch(ctx, patchObj, patch); err != nil {
		return controllerutil.OperationResultNone, err
	}
	return controllerutil.OperationResultUpdatedStatusOnly, nil
}

// mutate wraps a MutateFn and applies validation to its result.
func mutate(f controllerutil.MutateFn, key client.ObjectKey, obj client.Object) error {
	if err := f(); err != nil {
		return err
	}
	if newKey := client.ObjectKeyFromObject(obj); key != newKey {
		return fmt.Errorf("MutateFn cannot mutate object name and/or object namespace")
	}
	return nil
}
