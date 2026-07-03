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

package proxy

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// BasicInjector injects Authorization: Basic base64(username:secret).
type BasicInjector struct {
	username       string
	envVar         string
	defaultHeaders map[string]string
}

func NewBasicInjector(route *Route) (*BasicInjector, error) {
	if route.EnvVar == "" {
		return nil, fmt.Errorf("basic injector requires envVar")
	}
	if route.BasicUsername == "" {
		return nil, fmt.Errorf("basic injector requires basicUsername")
	}
	return &BasicInjector{
		username:       route.BasicUsername,
		envVar:         route.EnvVar,
		defaultHeaders: route.DefaultHeaders,
	}, nil
}

func (b *BasicInjector) Inject(req *http.Request) error {
	password := os.Getenv(b.envVar)
	if password == "" {
		return fmt.Errorf("credential env var %s is empty", b.envVar)
	}
	for k, v := range b.defaultHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		req.Header.Set(k, v)
	}
	value := base64.StdEncoding.EncodeToString([]byte(b.username + ":" + password))
	req.Header.Set("Authorization", "Basic "+value)
	return nil
}
