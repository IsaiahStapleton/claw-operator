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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type userSecretCache struct {
	ctx       context.Context
	reader    client.Reader
	namespace string
	secrets   map[string]*corev1.Secret
}

func newUserSecretCache(ctx context.Context, reader client.Reader, namespace string) *userSecretCache {
	return &userSecretCache{
		ctx:       ctx,
		reader:    reader,
		namespace: namespace,
		secrets:   map[string]*corev1.Secret{},
	}
}

func (c *userSecretCache) get(name string) (*corev1.Secret, error) {
	if secret, ok := c.secrets[name]; ok {
		return secret, nil
	}
	secret := &corev1.Secret{}
	if err := c.reader.Get(c.ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, secret); err != nil {
		return nil, err
	}
	c.secrets[name] = secret
	return secret, nil
}
