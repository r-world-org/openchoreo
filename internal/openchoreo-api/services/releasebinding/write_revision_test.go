// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func semanticWriteRevisionFixture() *openchoreov1alpha1.ReleaseBinding {
	return &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID("uid-1"),
			Labels:      map[string]string{"a": "1", "b": "2"},
			Annotations: map[string]string{"c": "3", "d": "4"},
		},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner:                           openchoreov1alpha1.ReleaseBindingOwner{ProjectName: "project", ComponentName: "component"},
			Environment:                     "dev",
			ReleaseName:                     "release-1",
			ComponentTypeEnvironmentConfigs: &runtime.RawExtension{Raw: []byte(`{"replicas":1,"nested":{"a":1,"b":2}}`)},
			TraitEnvironmentConfigs: map[string]runtime.RawExtension{
				"scaler": {Raw: []byte(`{"max":5,"min":1}`)},
			},
			WorkloadOverrides: &openchoreov1alpha1.WorkloadOverrideTemplateSpec{
				Container: &openchoreov1alpha1.ContainerOverride{
					Env: []openchoreov1alpha1.EnvVar{{Key: "MODE", Value: "safe"}},
				},
			},
			State: openchoreov1alpha1.ReleaseStateActive,
		},
	}
}

func mustSemanticWriteRevision(t *testing.T, rb *openchoreov1alpha1.ReleaseBinding) string {
	t.Helper()
	revision, err := SemanticWriteRevision(rb)
	require.NoError(t, err)
	return revision
}

func TestSemanticWriteRevision(t *testing.T) {
	t.Run("same writable state with different map and embedded JSON order", func(t *testing.T) {
		left := semanticWriteRevisionFixture()
		right := semanticWriteRevisionFixture()
		right.Labels = map[string]string{"b": "2", "a": "1"}
		right.Annotations = map[string]string{"d": "4", "c": "3"}
		right.Spec.ComponentTypeEnvironmentConfigs.Raw = []byte(`{"nested":{"b":2,"a":1},"replicas":1}`)
		assert.Equal(t, mustSemanticWriteRevision(t, left), mustSemanticWriteRevision(t, right))
	})

	t.Run("status only update is excluded", func(t *testing.T) {
		before := semanticWriteRevisionFixture()
		after := before.DeepCopy()
		after.Status.ObservedGeneration = 42
		after.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
		assert.Equal(t, mustSemanticWriteRevision(t, before), mustSemanticWriteRevision(t, after))
	})

	t.Run("server managed metadata is excluded", func(t *testing.T) {
		before := semanticWriteRevisionFixture()
		after := before.DeepCopy()
		after.ResourceVersion = "99"
		after.Generation = 12
		after.CreationTimestamp = metav1.Now()
		after.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
		assert.Equal(t, mustSemanticWriteRevision(t, before), mustSemanticWriteRevision(t, after))
	})

	tests := []struct {
		name   string
		mutate func(*openchoreov1alpha1.ReleaseBinding)
	}{
		{"releaseName", func(rb *openchoreov1alpha1.ReleaseBinding) { rb.Spec.ReleaseName = "release-2" }},
		{"componentTypeEnvironmentConfigs", func(rb *openchoreov1alpha1.ReleaseBinding) {
			rb.Spec.ComponentTypeEnvironmentConfigs.Raw = []byte(`{"replicas":2}`)
		}},
		{"traitEnvironmentConfigs", func(rb *openchoreov1alpha1.ReleaseBinding) {
			rb.Spec.TraitEnvironmentConfigs["scaler"] = runtime.RawExtension{Raw: []byte(`{"min":2,"max":5}`)}
		}},
		{"workloadOverrides", func(rb *openchoreov1alpha1.ReleaseBinding) {
			rb.Spec.WorkloadOverrides.Container.Env[0].Value = "fast"
		}},
		{"state", func(rb *openchoreov1alpha1.ReleaseBinding) { rb.Spec.State = openchoreov1alpha1.ReleaseStateUndeploy }},
		{"labels", func(rb *openchoreov1alpha1.ReleaseBinding) { rb.Labels["a"] = "changed" }},
		{"annotations", func(rb *openchoreov1alpha1.ReleaseBinding) { rb.Annotations["c"] = "changed" }},
		{"uid", func(rb *openchoreov1alpha1.ReleaseBinding) { rb.UID = types.UID("uid-2") }},
	}
	for _, tt := range tests {
		t.Run(tt.name+" change invalidates", func(t *testing.T) {
			before := semanticWriteRevisionFixture()
			after := before.DeepCopy()
			tt.mutate(after)
			assert.NotEqual(t, mustSemanticWriteRevision(t, before), mustSemanticWriteRevision(t, after))
		})
	}
}

func TestParseWriteRevision(t *testing.T) {
	valid := mustSemanticWriteRevision(t, semanticWriteRevisionFixture())
	parsed, err := ParseWriteRevision(valid)
	require.NoError(t, err)
	assert.Equal(t, valid, parsed)

	for _, value := range []string{
		"",
		valid + "," + valid,
		" " + valid,
		valid + " ",
		"wrong:" + valid[len(writeRevisionPrefix):],
		"rb-sha256:" + "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"rb-sha256:not-hex",
		"rb-sha256:" + "0",
		"rb-sha256:" + "0" + valid[len(writeRevisionPrefix):],
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseWriteRevision(value)
			require.ErrorIs(t, err, ErrInvalidReleaseBindingWriteRevision)
		})
	}
}
