/*
Copyright 2022 The Tekton Authors
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

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	resolverconfig "github.com/tektoncd/pipeline/pkg/apis/config/resolver"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/resolution/common"
	"github.com/tektoncd/pipeline/pkg/resolution/resolver/framework"
)

const (
	// LabelValueHubResolverType is the value to use for the
	// resolution.tekton.dev/type label on resource requests
	LabelValueHubResolverType string = "hub"

	disabledError = "cannot handle resolution request, enable-hub-resolver feature flag not true"
)

// Resolver implements a framework.Resolver that can fetch files from OCI bundles.
type Resolver struct {
	// HubURL is the URL for hub resolver
	HubURL string
}

// Initialize sets up any dependencies needed by the resolver. None atm.
func (r *Resolver) Initialize(context.Context) error {
	return nil
}

// GetName returns a string name to refer to this resolver by.
func (r *Resolver) GetName(context.Context) string {
	return "Hub"
}

// GetConfigName returns the name of the bundle resolver's configmap.
func (r *Resolver) GetConfigName(context.Context) string {
	return "hubresolver-config"
}

// GetSelector returns a map of labels to match requests to this resolver.
func (r *Resolver) GetSelector(context.Context) map[string]string {
	return map[string]string{
		common.LabelKeyResolverType: LabelValueHubResolverType,
	}
}

// ValidateParams ensures parameters from a request are as expected.
func (r *Resolver) ValidateParams(ctx context.Context, params []pipelinev1beta1.Param) error {
	if r.isDisabled(ctx) {
		return errors.New(disabledError)
	}
	paramsMap := make(map[string]pipelinev1beta1.ParamValue)
	for _, p := range params {
		paramsMap[p.Name] = p.Value
	}
	if _, ok := paramsMap[ParamName]; !ok {
		return errors.New("must include name param")
	}
	if _, ok := paramsMap[ParamVersion]; !ok {
		return errors.New("must include version param")
	}
	if kind, ok := paramsMap[ParamKind]; ok {
		if kind.StringVal != "task" && kind.StringVal != "pipeline" {
			return errors.New("kind param must be task or pipeline")
		}
	}
	return nil
}

type dataResponse struct {
	YAML string `json:"yaml"`
}

type hubResponse struct {
	Data dataResponse `json:"data"`
}

// Resolve uses the given params to resolve the requested file or resource.
func (r *Resolver) Resolve(ctx context.Context, params []pipelinev1beta1.Param) (framework.ResolvedResource, error) {
	if r.isDisabled(ctx) {
		return nil, errors.New(disabledError)
	}

	conf := framework.GetResolverConfigFromContext(ctx)

	paramsMap := make(map[string]string)
	for _, p := range params {
		paramsMap[p.Name] = p.Value.StringVal
	}

	if _, ok := paramsMap[ParamCatalog]; !ok {
		if catalogString, ok := conf[ConfigCatalog]; ok {
			paramsMap[ParamCatalog] = catalogString
		} else {
			return nil, fmt.Errorf("default catalog was not set during installation of the hub resolver")
		}
	}

	kind, ok := paramsMap[ParamKind]
	if !ok {
		if kindString, ok := conf[ConfigKind]; ok {
			kind = kindString
		} else {
			return nil, fmt.Errorf("default resource Kind was not set during installation of the hub resolver")
		}
	}
	if kind != "task" && kind != "pipeline" {
		return nil, fmt.Errorf("kind param must be task or pipeline")
	}

	paramsMap[ParamKind] = kind
	url := fmt.Sprintf(r.HubURL, paramsMap[ParamCatalog], paramsMap[ParamKind], paramsMap[ParamName], paramsMap[ParamVersion])
	// #nosec G107 -- URL cannot be constant in this case.
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error requesting resource from hub: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("requested resource '%s' not found on hub", url)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}
	hr := hubResponse{}
	err = json.Unmarshal(body, &hr)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling json response: %w", err)
	}
	return &ResolvedHubResource{
		Content: []byte(hr.Data.YAML),
	}, nil
}

// ResolvedHubResource wraps the data we want to return to Pipelines
type ResolvedHubResource struct {
	Content []byte
}

var _ framework.ResolvedResource = &ResolvedHubResource{}

// Data returns the bytes of our hard-coded Pipeline
func (rr *ResolvedHubResource) Data() []byte {
	return rr.Content
}

// Annotations returns any metadata needed alongside the data. None atm.
func (*ResolvedHubResource) Annotations() map[string]string {
	return nil
}

// Source is the source reference of the remote data that records where the remote
// file came from including the url, digest and the entrypoint.
func (rr *ResolvedHubResource) Source() *pipelinev1beta1.ConfigSource {
	return nil
}

func (r *Resolver) isDisabled(ctx context.Context) bool {
	cfg := resolverconfig.FromContextOrDefaults(ctx)
	if cfg.FeatureFlags.EnableHubResolver {
		return false
	}

	return true
}
