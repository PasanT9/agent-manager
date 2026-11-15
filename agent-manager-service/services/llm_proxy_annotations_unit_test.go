// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/wso2/agent-manager/agent-manager-service/models"
)

func TestAgentProxyAnnotations(t *testing.T) {
	assert.Equal(t, map[string]string{
		"gateway.api-platform.wso2.com/project-id": "api-platform",
		"gateway.api-platform.wso2.com/agent-id":   "hotel-booking",
	}, agentProxyAnnotations("api-platform", "hotel-booking"))
	assert.Nil(t, agentProxyAnnotations("", "hotel-booking"), "no project: nothing to name")
	assert.Nil(t, agentProxyAnnotations("api-platform", ""), "no agent: nothing to name")
}

func TestRenderLLMProxy_SetsTheAnnotationsOnItsMetadata(t *testing.T) {
	proxy := &models.LLMProxy{Configuration: models.LLMProxyConfig{Name: "hotel-booking-proxy"}}
	provider := &models.LLMProvider{Artifact: &models.Artifact{Handle: "openai"}}

	read := func(content string) map[string]any {
		var doc map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(content), &doc))
		return doc["metadata"].(map[string]any)
	}

	content, err := renderLLMProxy(proxy, provider, agentProxyAnnotations("api-platform", "hotel-booking"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"gateway.api-platform.wso2.com/project-id": "api-platform",
		"gateway.api-platform.wso2.com/agent-id":   "hotel-booking",
	}, read(content)["annotations"])

	content, err = renderLLMProxy(proxy, provider, nil)
	require.NoError(t, err)
	assert.NotContains(t, read(content), "annotations", "a proxy no single agent uses carries none")
}

func TestAgentAnnotations_UsesTheStoredAnnotations(t *testing.T) {
	stored := agentProxyAnnotations("api-platform", "hotel-booking")
	got, err := agentAnnotations(context.Background(), &models.LLMProxy{Configuration: models.LLMProxyConfig{Annotations: stored}})
	require.NoError(t, err)
	assert.Equal(t, stored, got, "stored annotations are used without a lookup")
}
