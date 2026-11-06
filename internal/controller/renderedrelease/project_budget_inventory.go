// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package renderedrelease

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	dpkubernetes "github.com/openchoreo/openchoreo/internal/dataplane/kubernetes"
	"github.com/openchoreo/openchoreo/internal/labels"
)

type projectBudgetTarget struct {
	gvk schema.GroupVersionKind
	key client.ObjectKey
}

// Values indicate whether the budget is in the current desired state.
type projectBudgetInventory map[projectBudgetTarget]bool

func isCellBudget(gvk schema.GroupVersionKind) bool {
	return gvk.Group == "" && gvk.Version == "v1" &&
		(gvk.Kind == "ResourceQuota" || gvk.Kind == "LimitRange")
}

func projectBindingOwner(release *openchoreov1alpha1.RenderedRelease) *metav1.OwnerReference {
	owner := metav1.GetControllerOf(release)
	if owner != nil && owner.Kind == "ProjectReleaseBinding" && owner.APIVersion == openchoreov1alpha1.GroupVersion.String() {
		return owner
	}
	return nil
}

func (r *Reconciler) projectBudgetTargets(ctx context.Context, release *openchoreov1alpha1.RenderedRelease, desired []*unstructured.Unstructured) (projectBudgetInventory, error) {
	owner := projectBindingOwner(release)
	if owner == nil {
		return nil, nil
	}
	targets := make(projectBudgetInventory)
	for _, observed := range release.Status.Resources {
		gvk := schema.GroupVersionKind{Group: observed.Group, Version: observed.Version, Kind: observed.Kind}
		if isCellBudget(gvk) {
			targets[projectBudgetTarget{gvk: gvk, key: client.ObjectKey{Namespace: observed.Namespace, Name: observed.Name}}] = false
		}
	}
	for _, obj := range desired {
		if gvk := obj.GroupVersionKind(); isCellBudget(gvk) {
			targets[projectBudgetTarget{gvk: gvk, key: client.ObjectKeyFromObject(obj)}] = true
		}
	}
	if len(targets) == 0 {
		return targets, nil
	}

	binding := &openchoreov1alpha1.ProjectReleaseBinding{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: release.Namespace, Name: owner.Name}, binding); err != nil {
		return nil, fmt.Errorf("read project budget owner: %w", err)
	}
	if binding.UID != owner.UID || binding.Spec.Owner.ProjectName != release.Spec.Owner.ProjectName ||
		binding.Spec.Environment != release.Spec.EnvironmentName {
		return nil, fmt.Errorf("project budget owner does not match the release cell")
	}
	cell := dpkubernetes.GenerateK8sNameWithLengthLimit(dpkubernetes.MaxNamespaceNameLength,
		"dp", binding.Namespace, binding.Spec.Owner.ProjectName, binding.Spec.Environment)
	for target := range targets {
		if target.key.Namespace != cell || target.key.Name == "" {
			return nil, fmt.Errorf("project budget %s is outside release cell %q", target.key, cell)
		}
	}
	return targets, nil
}

func (inventory projectBudgetInventory) read(ctx context.Context, planeClient client.Client, release *openchoreov1alpha1.RenderedRelease) ([]*unstructured.Unstructured, error) {
	var resources []*unstructured.Unstructured
	for target, desired := range inventory {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(target.gvk)
		if err := planeClient.Get(ctx, target.key, obj); err != nil {
			if apierrors.IsNotFound(err) && (!desired || !release.DeletionTimestamp.IsZero()) {
				continue
			}
			return nil, fmt.Errorf("read project budget %s %s: %w", target.gvk.Kind, target.key, err)
		}
		owned := obj.GetLabels()[labels.LabelKeyManagedBy] == ControllerName &&
			obj.GetLabels()[labels.LabelKeyRenderedReleaseUID] == string(release.UID)
		if !owned {
			if desired && release.DeletionTimestamp.IsZero() {
				return nil, fmt.Errorf("project budget %s is not owned by release %q", target.key, release.Name)
			}
			continue
		}
		resources = append(resources, obj)
	}
	return resources, nil
}

func (r *Reconciler) discoverDataPlaneCleanupResources(ctx context.Context, planeClient client.Client, release *openchoreov1alpha1.RenderedRelease) ([]*unstructured.Unstructured, error) {
	var inventory projectBudgetInventory
	if projectBindingOwner(release) != nil {
		desired, err := r.makeDesiredResources(release)
		if err != nil {
			return nil, err
		}
		inventory, err = r.projectBudgetTargets(ctx, release, desired)
		if err != nil {
			return nil, err
		}
	}
	gvks := findAllKnownGVKs(nil, release.Status.Resources, targetPlaneDataPlane)
	return r.listLiveResourcesByGVKs(ctx, planeClient, release, gvks, inventory)
}
