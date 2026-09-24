// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	testNamespace       = "test-ns"
	testProjectName     = "test-project"
	testComponentName   = "test-component"
	testEnvironmentName = "dev"
	testRBName          = "test-rb"
)

func newService(t *testing.T, objs ...client.Object) Service {
	t.Helper()
	return NewService(testutil.NewFakeClient(objs...), testutil.TestLogger())
}

func TestCreateReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		comp := testutil.NewComponent(testNamespace, testProjectName, testComponentName)
		svc := newService(t, comp)
		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)

		result, err := svc.CreateReleaseBinding(ctx, testNamespace, rb)
		require.NoError(t, err)
		assert.Equal(t, testNamespace, result.Namespace)
		assert.Equal(t, openchoreov1alpha1.ReleaseBindingStatus{}, result.Status)
		assert.Equal(t, releaseBindingTypeMeta, result.TypeMeta)
		assert.Equal(t, testProjectName, result.Labels[labels.LabelKeyProjectName])
		assert.Equal(t, testComponentName, result.Labels[labels.LabelKeyComponentName])
	})

	t.Run("nil input", func(t *testing.T) {
		comp := testutil.NewComponent(testNamespace, testProjectName, testComponentName)
		svc := newService(t, comp)

		_, err := svc.CreateReleaseBinding(ctx, testNamespace, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("already exists", func(t *testing.T) {
		comp := testutil.NewComponent(testNamespace, testProjectName, testComponentName)
		existing := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		svc := newService(t, comp, existing)

		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		_, err := svc.CreateReleaseBinding(ctx, testNamespace, rb)
		require.ErrorIs(t, err, ErrReleaseBindingAlreadyExists)
	})

	t.Run("component not found", func(t *testing.T) {
		svc := newService(t)
		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)

		_, err := svc.CreateReleaseBinding(ctx, testNamespace, rb)
		require.ErrorIs(t, err, ErrComponentNotFound)
	})

	t.Run("label normalization", func(t *testing.T) {
		comp := testutil.NewComponent(testNamespace, testProjectName, testComponentName)
		svc := newService(t, comp)

		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		rb.Labels = map[string]string{
			labels.LabelKeyProjectName:   "wrong",
			labels.LabelKeyComponentName: "wrong",
		}

		result, err := svc.CreateReleaseBinding(ctx, testNamespace, rb)
		require.NoError(t, err)
		assert.Equal(t, testProjectName, result.Labels[labels.LabelKeyProjectName])
		assert.Equal(t, testComponentName, result.Labels[labels.LabelKeyComponentName])
	})
}

func TestUpdateReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		existing := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		svc := newService(t, existing)

		update := &openchoreov1alpha1.ReleaseBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   testRBName,
				Labels: map[string]string{"custom": "value"},
			},
			Spec: existing.Spec,
		}

		result, err := svc.UpdateReleaseBinding(ctx, testNamespace, update, nil)
		require.NoError(t, err)
		assert.Equal(t, releaseBindingTypeMeta, result.TypeMeta)
		assert.Equal(t, openchoreov1alpha1.ReleaseBindingStatus{}, result.Status)
		assert.Equal(t, testProjectName, result.Labels[labels.LabelKeyProjectName])
		assert.Equal(t, testComponentName, result.Labels[labels.LabelKeyComponentName])
		assert.Equal(t, "value", result.Labels["custom"])
	})

	t.Run("nil input", func(t *testing.T) {
		svc := newService(t)

		_, err := svc.UpdateReleaseBinding(ctx, testNamespace, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(t)
		rb := &openchoreov1alpha1.ReleaseBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "nonexistent"},
		}

		_, err := svc.UpdateReleaseBinding(ctx, testNamespace, rb, nil)
		require.ErrorIs(t, err, ErrReleaseBindingNotFound)
	})
}

func TestConditionalUpdateReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("matching precondition updates fresh object and preserves admitted state", func(t *testing.T) {
		existing := semanticWriteRevisionFixture()
		existing.Name = testRBName
		existing.Namespace = testNamespace
		existing.ResourceVersion = "7"
		expected := mustSemanticWriteRevision(t, existing)
		update := existing.DeepCopy()
		update.Spec.ReleaseName = "release-2"

		svc := newService(t, existing)
		result, err := svc.UpdateReleaseBinding(ctx, testNamespace, update, &expected)
		require.NoError(t, err)
		assert.Equal(t, "release-2", result.Spec.ReleaseName)
		assert.Equal(t, "1", result.Labels["a"])
		assert.Equal(t, "2", result.Labels["b"])
		assert.Equal(t, existing.Spec.Owner.ProjectName, result.Labels[labels.LabelKeyProjectName])
		assert.Equal(t, existing.Spec.Owner.ComponentName, result.Labels[labels.LabelKeyComponentName])
		assert.Equal(t, existing.Annotations, result.Annotations)
		assert.Equal(t, existing.Spec.Owner, result.Spec.Owner)
		assert.Equal(t, existing.Spec.Environment, result.Spec.Environment)
		require.NotNil(t, result.Spec.ComponentTypeEnvironmentConfigs)
		assert.JSONEq(t, string(existing.Spec.ComponentTypeEnvironmentConfigs.Raw), string(result.Spec.ComponentTypeEnvironmentConfigs.Raw))
		assert.Equal(t, existing.Spec.TraitEnvironmentConfigs, result.Spec.TraitEnvironmentConfigs)
		assert.Equal(t, existing.Spec.WorkloadOverrides, result.Spec.WorkloadOverrides)
		assert.Equal(t, existing.Spec.State, result.Spec.State)
	})

	t.Run("stale precondition rejects without write", func(t *testing.T) {
		existing := semanticWriteRevisionFixture()
		existing.Name = testRBName
		existing.Namespace = testNamespace
		update := existing.DeepCopy()
		update.Spec.ReleaseName = "release-2"
		stale := "rb-sha256:0000000000000000000000000000000000000000000000000000000000000000"

		svc := newService(t, existing)
		_, err := svc.UpdateReleaseBinding(ctx, testNamespace, update, &stale)
		require.ErrorIs(t, err, ErrReleaseBindingPreconditionFailed)

		current, err := svc.GetReleaseBinding(ctx, testNamespace, testRBName)
		require.NoError(t, err)
		assert.Equal(t, "release-1", current.Spec.ReleaseName)
	})

	t.Run("Kubernetes conflict is returned once and newer state is preserved", func(t *testing.T) {
		existing := semanticWriteRevisionFixture()
		existing.Name = testRBName
		existing.Namespace = testNamespace
		existing.ResourceVersion = "7"
		expected := mustSemanticWriteRevision(t, existing)
		update := existing.DeepCopy()
		update.Spec.ReleaseName = "client-release"

		updateCalls := 0
		fc := fake.NewClientBuilder().
			WithScheme(testutil.NewScheme()).
			WithObjects(existing).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updateCalls++
					newer := &openchoreov1alpha1.ReleaseBinding{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), newer))
					newer.Spec.ReleaseName = "newer-release"
					require.NoError(t, c.Update(ctx, newer, opts...))
					return apierrors.NewConflict(
						schema.GroupResource{Group: openchoreov1alpha1.GroupVersion.Group, Resource: "releasebindings"},
						obj.GetName(),
						assert.AnError,
					)
				},
			}).
			Build()
		svc := NewService(fc, testutil.TestLogger())

		_, err := svc.UpdateReleaseBinding(ctx, testNamespace, update, &expected)
		require.ErrorIs(t, err, ErrReleaseBindingConflict)
		assert.Equal(t, 1, updateCalls, "provider must not retry a conflicting update")

		newer := &openchoreov1alpha1.ReleaseBinding{}
		require.NoError(t, fc.Get(ctx, types.NamespacedName{Name: testRBName, Namespace: testNamespace}, newer))
		assert.Equal(t, "newer-release", newer.Spec.ReleaseName)
	})
}

func TestListReleaseBindings(t *testing.T) {
	ctx := context.Background()

	t.Run("success with items", func(t *testing.T) {
		rb1 := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, "rb-1")
		rb2 := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, "staging", "rb-2")
		svc := newService(t, rb1, rb2)

		result, err := svc.ListReleaseBindings(ctx, testNamespace, "", services.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		for _, item := range result.Items {
			assert.Equal(t, releaseBindingTypeMeta, item.TypeMeta)
		}
	})

	t.Run("empty", func(t *testing.T) {
		svc := newService(t)

		result, err := svc.ListReleaseBindings(ctx, testNamespace, "", services.ListOptions{})
		require.NoError(t, err)
		assert.Empty(t, result.Items)
	})

	t.Run("with component filter", func(t *testing.T) {
		rb1 := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, "rb-1")
		rb2 := testutil.NewReleaseBinding(testNamespace, testProjectName, "other-component", testEnvironmentName, "rb-2")
		svc := newService(t, rb1, rb2)

		result, err := svc.ListReleaseBindings(ctx, testNamespace, testComponentName, services.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, result.Items, 1)
		assert.Equal(t, testComponentName, result.Items[0].Spec.Owner.ComponentName)
	})

	t.Run("namespace isolation", func(t *testing.T) {
		rbInNs := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, "rb-in")
		rbOtherNs := testutil.NewReleaseBinding("other-ns", testProjectName, testComponentName, testEnvironmentName, "rb-out")
		svc := newService(t, rbInNs, rbOtherNs)

		result, err := svc.ListReleaseBindings(ctx, testNamespace, "", services.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, result.Items, 1)
		assert.Equal(t, "rb-in", result.Items[0].Name)
	})

	t.Run("invalid label selector", func(t *testing.T) {
		svc := newService(t)

		_, err := svc.ListReleaseBindings(ctx, testNamespace, "", services.ListOptions{LabelSelector: "===invalid"})
		require.Error(t, err)
		var validationErr *services.ValidationError
		assert.ErrorAs(t, err, &validationErr)
	})
}

func TestGetReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		svc := newService(t, rb)

		result, err := svc.GetReleaseBinding(ctx, testNamespace, testRBName)
		require.NoError(t, err)
		assert.Equal(t, releaseBindingTypeMeta, result.TypeMeta)
		assert.Equal(t, testRBName, result.Name)
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(t)

		_, err := svc.GetReleaseBinding(ctx, testNamespace, "nonexistent")
		require.ErrorIs(t, err, ErrReleaseBindingNotFound)
	})
}

func TestDeleteReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		rb := testutil.NewReleaseBinding(testNamespace, testProjectName, testComponentName, testEnvironmentName, testRBName)
		svc := newService(t, rb)

		err := svc.DeleteReleaseBinding(ctx, testNamespace, testRBName)
		require.NoError(t, err)

		_, err = svc.GetReleaseBinding(ctx, testNamespace, testRBName)
		require.ErrorIs(t, err, ErrReleaseBindingNotFound)
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(t)

		err := svc.DeleteReleaseBinding(ctx, testNamespace, "nonexistent")
		require.ErrorIs(t, err, ErrReleaseBindingNotFound)
	})
}
