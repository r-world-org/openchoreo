// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
	releasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/releasebinding"
)

// rbBundle holds the real HTTP handler wired to a fake K8s client so tests can
// both drive the handler through HTTP and inspect the resulting K8s state.
type rbBundle struct {
	handler    http.Handler
	fakeClient client.Client
}

func doRequestWithWriteRevision(t *testing.T, h http.Handler, path string, body []byte, revision string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-OpenChoreo-Write-Revision", revision)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return req, rec
}

// newRBBundle builds an rbBundle seeded with the given objects and using the
// supplied PDP for authorization decisions.
func newRBBundle(t *testing.T, objects []client.Object, pdp authzcore.PDP) rbBundle {
	t.Helper()
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(objects...).
		Build()
	svc := releasebindingsvc.NewServiceWithAuthz(fc, pdp, slog.Default())
	services := &handlerservices.Services{ReleaseBindingService: svc}
	return rbBundle{
		handler:    newTestHTTPHandler(t, services),
		fakeClient: fc,
	}
}

// seedReleaseBinding returns an openchoreov1alpha1.ReleaseBinding seeded with Name,
// Namespace, and an Owner pointing to "test-comp".
func seedReleaseBinding(name string) *openchoreov1alpha1.ReleaseBinding {
	return &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
		},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner: openchoreov1alpha1.ReleaseBindingOwner{
				ComponentName: "test-comp",
				ProjectName:   "test-proj",
			},
			Environment: "dev",
		},
	}
}

// newReleaseBindingBody returns a gen.ReleaseBinding body suitable for HTTP create/update requests.
func newReleaseBindingBody(name string) *gen.ReleaseBinding {
	return &gen.ReleaseBinding{
		Metadata: gen.ObjectMeta{Name: name},
		Spec: &gen.ReleaseBindingSpec{
			Owner: struct {
				ComponentName string `json:"componentName"`
				ProjectName   string `json:"projectName"`
			}{
				ComponentName: "test-comp",
				ProjectName:   "test-proj",
			},
			Environment: "dev",
		},
	}
}

// seedComponentForRB returns a Component object used to satisfy the releasebinding
// service's component-existence validation on create/update.
func seedComponentForRB() *openchoreov1alpha1.Component {
	return &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "test-comp", Namespace: testNS},
	}
}

// --- List ---

func TestReleaseBindingHTTPList(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{
		seedReleaseBinding("rb-a"),
		seedReleaseBinding("rb-b"),
	}, &allowAllPDP{})

	req, rec := doRequest(t, bundle.handler, http.MethodGet,
		"/api/v1/namespaces/"+testNS+"/releasebindings", nil)

	assert.Equal(t, http.StatusOK, rec.Code)

	bodyBytes := rec.Body.Bytes()
	var resp gen.ReleaseBindingList
	require.NoError(t, json.Unmarshal(bodyBytes, &resp), "response body must be valid JSON")
	assert.Len(t, resp.Items, 2, "list must return both seeded release bindings")

	names := make([]string, len(resp.Items))
	for i, item := range resp.Items {
		names[i] = item.Metadata.Name
	}
	assert.ElementsMatch(t, []string{"rb-a", "rb-b"}, names)

	// Concern 2: response must conform to the OpenAPI contract.
	assertConformsToSpec(t, req, rec.Code, rec.Result().Header, bodyBytes)
}

func TestReleaseBindingHTTPListEmpty(t *testing.T) {
	bundle := newRBBundle(t, nil, &allowAllPDP{})

	req, rec := doRequest(t, bundle.handler, http.MethodGet,
		"/api/v1/namespaces/"+testNS+"/releasebindings", nil)

	assert.Equal(t, http.StatusOK, rec.Code)

	bodyBytes := rec.Body.Bytes()
	var resp gen.ReleaseBindingList
	require.NoError(t, json.Unmarshal(bodyBytes, &resp))
	assert.Empty(t, resp.Items, "empty store must return an empty items array")

	assertConformsToSpec(t, req, rec.Code, rec.Result().Header, bodyBytes)
}

// --- Get ---

func TestReleaseBindingHTTPGet(t *testing.T) {
	seed := seedReleaseBinding("rb-1")
	bundle := newRBBundle(t, []client.Object{seed}, &allowAllPDP{})

	req, rec := doRequest(t, bundle.handler, http.MethodGet,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", nil)

	assert.Equal(t, http.StatusOK, rec.Code)

	bodyBytes := rec.Body.Bytes()
	var resp gen.ReleaseBinding
	require.NoError(t, json.Unmarshal(bodyBytes, &resp))
	assert.Equal(t, "rb-1", resp.Metadata.Name)
	expectedRevision, err := releasebindingsvc.SemanticWriteRevision(seed)
	require.NoError(t, err)
	assert.Equal(t, expectedRevision, rec.Header().Get("OpenChoreo-Write-Revision"))

	assertConformsToSpec(t, req, rec.Code, rec.Result().Header, bodyBytes)
}

func TestReleaseBindingHTTPGetNotFound(t *testing.T) {
	bundle := newRBBundle(t, nil, &allowAllPDP{})

	_, rec := doRequest(t, bundle.handler, http.MethodGet,
		"/api/v1/namespaces/"+testNS+"/releasebindings/missing", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReleaseBindingHTTPGetForbidden(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedReleaseBinding("rb-1")}, &denyAllPDP{})

	_, rec := doRequest(t, bundle.handler, http.MethodGet,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", nil)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- Create ---

func TestReleaseBindingHTTPCreate(t *testing.T) {
	// Seed a Component so the service can resolve the component reference.
	bundle := newRBBundle(t, []client.Object{seedComponentForRB()}, &allowAllPDP{})

	body, _ := json.Marshal(newReleaseBindingBody("rb-1"))
	req, rec := doRequest(t, bundle.handler, http.MethodPost,
		"/api/v1/namespaces/"+testNS+"/releasebindings", body)

	assert.Equal(t, http.StatusCreated, rec.Code)

	bodyBytes := rec.Body.Bytes()
	var resp gen.ReleaseBinding
	require.NoError(t, json.Unmarshal(bodyBytes, &resp))
	assert.Equal(t, "rb-1", resp.Metadata.Name)

	// Concern 3: verify the object was actually persisted to the fake K8s store.
	var k8sRB openchoreov1alpha1.ReleaseBinding
	err := bundle.fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "rb-1", Namespace: testNS}, &k8sRB)
	require.NoError(t, err, "release binding must be persisted to K8s after creation")
	assert.Equal(t, "rb-1", k8sRB.Name)

	// Concern 2: validate against OpenAPI contract.
	assertConformsToSpec(t, req, rec.Code, rec.Result().Header, bodyBytes)
}

func TestReleaseBindingHTTPCreateAlreadyExists(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedComponentForRB(), seedReleaseBinding("rb-1")}, &allowAllPDP{})

	body, _ := json.Marshal(newReleaseBindingBody("rb-1"))
	_, rec := doRequest(t, bundle.handler, http.MethodPost,
		"/api/v1/namespaces/"+testNS+"/releasebindings", body)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestReleaseBindingHTTPCreateForbidden(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedComponentForRB()}, &denyAllPDP{})

	body, _ := json.Marshal(newReleaseBindingBody("rb-1"))
	_, rec := doRequest(t, bundle.handler, http.MethodPost,
		"/api/v1/namespaces/"+testNS+"/releasebindings", body)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- Update ---

func TestReleaseBindingHTTPUpdate(t *testing.T) {
	// Seed both the component (for validation) and the existing release binding.
	bundle := newRBBundle(t, []client.Object{seedComponentForRB(), seedReleaseBinding("rb-1")}, &allowAllPDP{})

	// Include spec.owner + spec.environment so we can assert they are preserved,
	// and add a label to assert label persistence.
	body, _ := json.Marshal(gen.ReleaseBinding{
		Metadata: gen.ObjectMeta{
			Name:   "rb-1",
			Labels: &map[string]string{"tier": "updated"},
		},
		Spec: &gen.ReleaseBindingSpec{
			Owner: struct {
				ComponentName string `json:"componentName"`
				ProjectName   string `json:"projectName"`
			}{
				ComponentName: "test-comp",
				ProjectName:   "test-proj",
			},
			Environment: "dev",
		},
	})

	req, rec := doRequest(t, bundle.handler, http.MethodPut,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", body)

	assert.Equal(t, http.StatusOK, rec.Code)

	bodyBytes := rec.Body.Bytes()
	var resp gen.ReleaseBinding
	require.NoError(t, json.Unmarshal(bodyBytes, &resp))
	assert.Equal(t, "rb-1", resp.Metadata.Name)
	require.NotNil(t, resp.Spec, "response spec must not be nil")
	assert.Equal(t, "test-comp", resp.Spec.Owner.ComponentName,
		"owner.componentName must be preserved in response")
	assert.Equal(t, "dev", resp.Spec.Environment,
		"environment must be preserved in response")

	// Concern 3: verify label and spec fields are reflected in the fake K8s store.
	var k8sRB openchoreov1alpha1.ReleaseBinding
	err := bundle.fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "rb-1", Namespace: testNS}, &k8sRB)
	require.NoError(t, err, "release binding must still exist in K8s after update")
	assert.Equal(t, "updated", k8sRB.Labels["tier"],
		"updated label must be persisted to K8s")
	assert.Equal(t, "test-comp", k8sRB.Spec.Owner.ComponentName,
		"owner.componentName must be persisted to K8s after update")
	assert.Equal(t, "dev", k8sRB.Spec.Environment,
		"environment must be persisted to K8s after update")

	// Concern 2: validate against OpenAPI contract.
	assertConformsToSpec(t, req, rec.Code, rec.Result().Header, bodyBytes)
}

func TestReleaseBindingHTTPUpdateNotFound(t *testing.T) {
	bundle := newRBBundle(t, nil, &allowAllPDP{})

	body, _ := json.Marshal(gen.ReleaseBinding{Metadata: gen.ObjectMeta{Name: "nonexistent"}})
	_, rec := doRequest(t, bundle.handler, http.MethodPut,
		"/api/v1/namespaces/"+testNS+"/releasebindings/nonexistent", body)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReleaseBindingHTTPUpdateForbidden(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedComponentForRB(), seedReleaseBinding("rb-1")}, &denyAllPDP{})

	body, _ := json.Marshal(gen.ReleaseBinding{Metadata: gen.ObjectMeta{Name: "rb-1"}})
	_, rec := doRequest(t, bundle.handler, http.MethodPut,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", body)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestReleaseBindingHTTPConditionalUpdate(t *testing.T) {
	path := "/api/v1/namespaces/" + testNS + "/releasebindings/rb-1"

	t.Run("exact write revision succeeds and returns the updated revision", func(t *testing.T) {
		seed := seedReleaseBinding("rb-1")
		seed.Spec.ReleaseName = "release-1"
		bundle := newRBBundle(t, []client.Object{seed}, &allowAllPDP{})
		expected, err := releasebindingsvc.SemanticWriteRevision(seed)
		require.NoError(t, err)

		bodyObj := newReleaseBindingBody("rb-1")
		bodyObj.Spec.ReleaseName = ptr.To("release-2")
		body, err := json.Marshal(bodyObj)
		require.NoError(t, err)
		req, rec := doRequestWithWriteRevision(t, bundle.handler, path, body, expected)

		require.Equal(t, http.StatusOK, rec.Code)
		var response gen.ReleaseBinding
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.NotNil(t, response.Spec)
		require.Equal(t, "release-2", *response.Spec.ReleaseName)
		updatedRevision := rec.Header().Get("OpenChoreo-Write-Revision")
		assert.NotEmpty(t, updatedRevision)
		assert.NotEqual(t, expected, updatedRevision)
		assertConformsToSpec(t, req, rec.Code, rec.Result().Header, rec.Body.Bytes())
	})

	t.Run("stale write revision returns 412 without mutation", func(t *testing.T) {
		seed := seedReleaseBinding("rb-1")
		seed.Spec.ReleaseName = "release-1"
		bundle := newRBBundle(t, []client.Object{seed}, &allowAllPDP{})
		stale := "rb-sha256:0000000000000000000000000000000000000000000000000000000000000000"
		bodyObj := newReleaseBindingBody("rb-1")
		bodyObj.Spec.ReleaseName = ptr.To("release-2")
		body, err := json.Marshal(bodyObj)
		require.NoError(t, err)

		_, rec := doRequestWithWriteRevision(t, bundle.handler, path, body, stale)
		require.Equal(t, http.StatusPreconditionFailed, rec.Code)
		var current openchoreov1alpha1.ReleaseBinding
		require.NoError(t, bundle.fakeClient.Get(context.Background(), types.NamespacedName{Name: "rb-1", Namespace: testNS}, &current))
		assert.Equal(t, "release-1", current.Spec.ReleaseName)
	})

	invalid := map[string]string{
		"empty":               "",
		"wrong-prefix":        "other:0000000000000000000000000000000000000000000000000000000000000000",
		"uppercase":           "rb-sha256:ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"wrong-length-short":  "rb-sha256:0",
		"wrong-length-long":   "rb-sha256:00000000000000000000000000000000000000000000000000000000000000000",
		"comma-list":          "rb-sha256:0000000000000000000000000000000000000000000000000000000000000000,rb-sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"leading-whitespace":  " rb-sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"trailing-whitespace": "rb-sha256:0000000000000000000000000000000000000000000000000000000000000000 ",
	}
	for name, value := range invalid {
		t.Run("malformed "+name+" returns 400 without mutation", func(t *testing.T) {
			seed := seedReleaseBinding("rb-1")
			seed.Spec.ReleaseName = "release-1"
			bundle := newRBBundle(t, []client.Object{seed}, &allowAllPDP{})
			bodyObj := newReleaseBindingBody("rb-1")
			bodyObj.Spec.ReleaseName = ptr.To("release-2")
			body, err := json.Marshal(bodyObj)
			require.NoError(t, err)

			_, rec := doRequestWithWriteRevision(t, bundle.handler, path, body, value)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			var current openchoreov1alpha1.ReleaseBinding
			require.NoError(t, bundle.fakeClient.Get(context.Background(), types.NamespacedName{Name: "rb-1", Namespace: testNS}, &current))
			assert.Equal(t, "release-1", current.Spec.ReleaseName)
		})
	}

	t.Run("authorization denial precedes a valid precondition and does not mutate", func(t *testing.T) {
		seed := seedReleaseBinding("rb-1")
		seed.Spec.ReleaseName = "release-1"
		expected, err := releasebindingsvc.SemanticWriteRevision(seed)
		require.NoError(t, err)
		bundle := newRBBundle(t, []client.Object{seed}, &denyAllPDP{})
		bodyObj := newReleaseBindingBody("rb-1")
		bodyObj.Spec.ReleaseName = ptr.To("release-2")
		body, err := json.Marshal(bodyObj)
		require.NoError(t, err)

		_, rec := doRequestWithWriteRevision(t, bundle.handler, path, body, expected)
		require.Equal(t, http.StatusForbidden, rec.Code)
		var current openchoreov1alpha1.ReleaseBinding
		require.NoError(t, bundle.fakeClient.Get(context.Background(), types.NamespacedName{Name: "rb-1", Namespace: testNS}, &current))
		assert.Equal(t, "release-1", current.Spec.ReleaseName)
	})

	t.Run("releaseName-only update preserves all other admitted mutable state", func(t *testing.T) {
		seed := seedReleaseBinding("rb-1")
		seed.UID = types.UID("uid-1")
		seed.Labels = map[string]string{
			labels.LabelKeyProjectName:   "test-proj",
			labels.LabelKeyComponentName: "test-comp",
			"team":                       "payments",
		}
		seed.Annotations = map[string]string{"note": "preserve"}
		seed.Spec.ReleaseName = "release-1"
		seed.Spec.ComponentTypeEnvironmentConfigs = &runtime.RawExtension{Raw: []byte(`{"replicas":2}`)}
		seed.Spec.TraitEnvironmentConfigs = map[string]runtime.RawExtension{"scaler": {Raw: []byte(`{"max":5}`)}}
		seed.Spec.WorkloadOverrides = &openchoreov1alpha1.WorkloadOverrideTemplateSpec{
			Container: &openchoreov1alpha1.ContainerOverride{Env: []openchoreov1alpha1.EnvVar{{Key: "MODE", Value: "safe"}}},
		}
		seed.Spec.State = openchoreov1alpha1.ReleaseStateUndeploy
		expectedRevision, err := releasebindingsvc.SemanticWriteRevision(seed)
		require.NoError(t, err)
		bundle := newRBBundle(t, []client.Object{seed}, &allowAllPDP{})

		bodyObj, err := convert[openchoreov1alpha1.ReleaseBinding, gen.ReleaseBinding](*seed.DeepCopy())
		require.NoError(t, err)
		bodyObj.Spec.ReleaseName = ptr.To("release-2")
		body, err := json.Marshal(bodyObj)
		require.NoError(t, err)
		_, rec := doRequestWithWriteRevision(t, bundle.handler, path, body, expectedRevision)
		require.Equal(t, http.StatusOK, rec.Code)

		var current openchoreov1alpha1.ReleaseBinding
		require.NoError(t, bundle.fakeClient.Get(context.Background(), types.NamespacedName{Name: "rb-1", Namespace: testNS}, &current))
		assert.Equal(t, "release-2", current.Spec.ReleaseName)
		assert.Equal(t, seed.Labels, current.Labels)
		assert.Equal(t, seed.Annotations, current.Annotations)
		assert.Equal(t, seed.Spec.Owner, current.Spec.Owner)
		assert.Equal(t, seed.Spec.Environment, current.Spec.Environment)
		assert.Equal(t, seed.Spec.ComponentTypeEnvironmentConfigs, current.Spec.ComponentTypeEnvironmentConfigs)
		assert.Equal(t, seed.Spec.TraitEnvironmentConfigs, current.Spec.TraitEnvironmentConfigs)
		assert.Equal(t, seed.Spec.WorkloadOverrides, current.Spec.WorkloadOverrides)
		assert.Equal(t, seed.Spec.State, current.Spec.State)
	})
}

func TestReleaseBindingHTTPKubernetesConflict(t *testing.T) {
	seed := seedReleaseBinding("rb-1")
	seed.Spec.ReleaseName = "release-1"
	expected, err := releasebindingsvc.SemanticWriteRevision(seed)
	require.NoError(t, err)
	updateCalls := 0
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(seed).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				updateCalls++
				return apierrors.NewConflict(
					schema.GroupResource{Group: openchoreov1alpha1.GroupVersion.Group, Resource: "releasebindings"},
					"rb-1",
					assert.AnError,
				)
			},
		}).
		Build()
	svc := releasebindingsvc.NewServiceWithAuthz(fc, &allowAllPDP{}, slog.Default())
	handler := newTestHTTPHandler(t, &handlerservices.Services{ReleaseBindingService: svc})
	bodyObj := newReleaseBindingBody("rb-1")
	bodyObj.Spec.ReleaseName = ptr.To("release-2")
	body, err := json.Marshal(bodyObj)
	require.NoError(t, err)

	_, rec := doRequestWithWriteRevision(t, handler,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", body, expected)
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, 1, updateCalls, "provider must not retry a conflicting Kubernetes update")
}

// --- Delete ---

func TestReleaseBindingHTTPDelete(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedReleaseBinding("rb-1")}, &allowAllPDP{})

	_, rec := doRequest(t, bundle.handler, http.MethodDelete,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", nil)

	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Concern 3: confirm the object is gone from the fake K8s store.
	var gone openchoreov1alpha1.ReleaseBinding
	err := bundle.fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "rb-1", Namespace: testNS}, &gone)
	require.True(t, apierrors.IsNotFound(err),
		"release binding must be removed from K8s after deletion, got err: %v", err)
}

func TestReleaseBindingHTTPDeleteNotFound(t *testing.T) {
	bundle := newRBBundle(t, nil, &allowAllPDP{})

	_, rec := doRequest(t, bundle.handler, http.MethodDelete,
		"/api/v1/namespaces/"+testNS+"/releasebindings/missing", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReleaseBindingHTTPDeleteForbidden(t *testing.T) {
	bundle := newRBBundle(t, []client.Object{seedReleaseBinding("rb-1")}, &denyAllPDP{})

	_, rec := doRequest(t, bundle.handler, http.MethodDelete,
		"/api/v1/namespaces/"+testNS+"/releasebindings/rb-1", nil)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}
