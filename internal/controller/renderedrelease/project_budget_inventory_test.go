// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package renderedrelease

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/labels"
)

type budgetReadClient struct {
	client.Client
	objects map[client.ObjectKey]*unstructured.Unstructured
	err     error
	gets    int
	lists   int
}

func (c *budgetReadClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	c.gets++
	if c.err != nil {
		return c.err
	}
	value, ok := c.objects[key]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: obj.GetObjectKind().GroupVersionKind().Kind}, key.Name)
	}
	value.DeepCopyInto(obj.(*unstructured.Unstructured))
	return nil
}

func (c *budgetReadClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	c.lists++
	return errors.New("quota and limit range LIST is forbidden")
}

func TestProjectBudgetInventoryUsesOnlyNamedCellReads(t *testing.T) {
	ctx := context.Background()
	const candidate = "dp-r-company-r-company-candidate-9f46ad27"
	const stable = "dp-r-company-r-company-stable-20c0f86c"
	scheme := runtime.NewScheme()
	if err := openchoreov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	binding := &openchoreov1alpha1.ProjectReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "r-company-candidate", Namespace: "r-company", UID: "binding"},
		Spec: openchoreov1alpha1.ProjectReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ProjectReleaseBindingOwner{ProjectName: "r-company"},
			Environment: "candidate",
		},
	}
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(binding).Build()}
	release := &openchoreov1alpha1.RenderedRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p-r-company-candidate", Namespace: "r-company", UID: "release",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(binding, openchoreov1alpha1.GroupVersion.WithKind("ProjectReleaseBinding"))},
		},
		Spec: openchoreov1alpha1.RenderedReleaseSpec{
			Owner:           openchoreov1alpha1.RenderedReleaseOwner{ProjectName: "r-company"},
			EnvironmentName: "candidate",
		},
	}
	quota := newBudgetObject(candidate, "r-company-budget", "ResourceQuota")
	limit := newBudgetObject(candidate, "r-company-container-limits", "LimitRange")
	gvks := []schema.GroupVersionKind{quota.GroupVersionKind(), limit.GroupVersionKind()}
	targets, err := r.projectBudgetTargets(ctx, release, []*unstructured.Unstructured{quota, limit})
	if err != nil {
		t.Fatal(err)
	}
	plane := &budgetReadClient{objects: map[client.ObjectKey]*unstructured.Unstructured{
		client.ObjectKeyFromObject(quota): quota,
		client.ObjectKeyFromObject(limit): limit,
	}}
	live, err := r.listLiveResourcesByGVKs(ctx, plane, release, gvks, targets)
	if err != nil || len(live) != 2 || plane.gets != 2 || plane.lists != 0 {
		t.Fatalf("expected two named reads and no LIST, got %d objects, %d GET, %d LIST, error %v", len(live), plane.gets, plane.lists, err)
	}

	t.Run("wrong or unauthorized cell", func(t *testing.T) {
		for _, namespace := range []string{stable, "plane-ce", ""} {
			foreign := quota.DeepCopy()
			foreign.SetNamespace(namespace)
			if _, err := r.projectBudgetTargets(ctx, release, []*unstructured.Unstructured{foreign}); err == nil {
				t.Errorf("accepted quota outside the owning cell: %q", namespace)
			}
		}
		forgedRelease := release.DeepCopy()
		forgedRelease.Spec.EnvironmentName = "stable"
		if _, err := r.projectBudgetTargets(ctx, forgedRelease, []*unstructured.Unstructured{newBudgetObject(stable, quota.GetName(), "ResourceQuota")}); err == nil {
			t.Fatal("accepted a release whose environment disagrees with its binding")
		}
	})

	t.Run("missing desired object", func(t *testing.T) {
		missing := &budgetReadClient{objects: map[client.ObjectKey]*unstructured.Unstructured{client.ObjectKeyFromObject(quota): quota}}
		if _, err := r.listLiveResourcesByGVKs(ctx, missing, release, gvks, targets); !apierrors.IsNotFound(err) || missing.lists != 0 {
			t.Fatalf("expected missing LimitRange to stop readiness, got %v", err)
		}
	})

	t.Run("API timeout stays an error", func(t *testing.T) {
		timeout := &budgetReadClient{err: context.DeadlineExceeded}
		if _, err := r.listLiveResourcesByGVKs(ctx, timeout, release, gvks, targets); !errors.Is(err, context.DeadlineExceeded) || timeout.lists != 0 {
			t.Fatalf("expected timeout without LIST fallback, got %v", err)
		}
	})

	t.Run("stale budgets must have the original ownership marker", func(t *testing.T) {
		observed := release.DeepCopy()
		observed.Status.Resources = []openchoreov1alpha1.RenderedManifestStatus{
			{Version: "v1", Kind: "ResourceQuota", Name: quota.GetName(), Namespace: candidate},
		}
		stale, err := r.projectBudgetTargets(ctx, observed, nil)
		if err != nil {
			t.Fatal(err)
		}
		foreign := quota.DeepCopy()
		foreign.SetLabels(nil)
		foreignPlane := &budgetReadClient{objects: map[client.ObjectKey]*unstructured.Unstructured{client.ObjectKeyFromObject(quota): foreign}}
		live, err := r.listLiveResourcesByGVKs(ctx, foreignPlane, observed, gvks, stale)
		if err != nil || len(live) != 0 || foreignPlane.lists != 0 {
			t.Fatalf("foreign budget leaked into cleanup inventory: %v, %v", live, err)
		}
	})
}

func newBudgetObject(namespace, name, kind string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind(kind)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetLabels(map[string]string{
		labels.LabelKeyManagedBy:          ControllerName,
		labels.LabelKeyRenderedReleaseUID: "release",
	})
	return obj
}
