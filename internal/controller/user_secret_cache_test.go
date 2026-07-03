/*
Copyright 2026 Red Hat.

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

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestUserSecretCacheGetMemoizesSecrets(t *testing.T) {
	reader := &countingSecretReader{
		secrets: map[string]*corev1.Secret{
			"test-token": {
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-token",
					Namespace:       "test-namespace",
					ResourceVersion: "12345",
				},
			},
		},
	}
	cache := newUserSecretCache(context.Background(), reader, "test-namespace")

	first, err := cache.get("test-token")
	require.NoError(t, err)
	second, err := cache.get("test-token")
	require.NoError(t, err)

	assert.Equal(t, 1, reader.calls)
	assert.Same(t, first, second)
	assert.Equal(t, "12345", second.ResourceVersion)
}

func TestUserSecretCacheGetDoesNotCacheErrors(t *testing.T) {
	reader := &countingSecretReader{
		err: apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing-token"),
	}
	cache := newUserSecretCache(context.Background(), reader, "test-namespace")

	_, err := cache.get("missing-token")
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
	assert.Empty(t, cache.secrets)

	_, err = cache.get("missing-token")
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
	assert.Equal(t, 2, reader.calls)
	assert.Empty(t, cache.secrets)
}

type countingSecretReader struct {
	calls   int
	secrets map[string]*corev1.Secret
	err     error
}

func (r *countingSecretReader) Get(
	_ context.Context,
	key client.ObjectKey,
	obj client.Object,
	_ ...client.GetOption,
) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	secret, ok := r.secrets[key.Name]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
	}
	secret.DeepCopyInto(obj.(*corev1.Secret))
	return nil
}

func (r *countingSecretReader) List(
	_ context.Context,
	_ client.ObjectList,
	_ ...client.ListOption,
) error {
	return nil
}
