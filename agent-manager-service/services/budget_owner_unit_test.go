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
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/orgctx"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// projectBudgetRequest is a budget of project api-platform: each of its agents gets $100.
func projectBudgetRequest(name string) spec.BudgetRequest {
	return spec.BudgetRequest{
		Name: name, OrgId: budgetTestOrgName, ProjectId: strPtr("api-platform"), DisplayName: strPtr(name),
		Limit:     spec.BudgetLimit{Amount: "100", Period: "month", Action: "block"},
		Consumers: []spec.BudgetConsumer{{Type: "agent", Allocation: strPtr("separate")}},
	}
}

func TestBudgetService_AProjectBudgetCountsOnlyItsProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)

	require.Len(t, env.created, 1)
	b := env.created[0]
	require.NotNil(t, b.ProjectID)
	assert.Equal(t, budgetTestProjectID, b.ProjectID.String(), "the owning project is stored by ID")
	require.Len(t, b.Configuration.Consumers, 2)
	assert.Equal(t, "agent", b.Configuration.Consumers[0].Type)
	project := b.Configuration.Consumers[1]
	assert.Equal(t, "project", project.Type, "the implied project consumer is added")
	assert.Equal(t, "shared", project.Allocation)
	assert.Equal(t, "api-platform", *project.ID)

	assert.Equal(t, budgetTestOrgName, resp.OrgId)
	require.NotNil(t, resp.ProjectId)
	assert.Equal(t, "api-platform", *resp.ProjectId)

	kind := BuildBudgetKind(b.Name, b.Configuration, true)
	out, err := json.Marshal(kind)
	require.NoError(t, err)
	assert.Contains(t, string(out), `{"type":"project","allocation":"shared","id":"api-platform"}`,
		"gateways get the project as a consumer")
}

func TestBudgetService_AnOrganizationBudgetHasNoProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)
	assert.Nil(t, env.created[0].ProjectID)
	assert.Equal(t, budgetTestOrgName, resp.OrgId)
	assert.Nil(t, resp.ProjectId)
	out, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "projectId")
}

func TestBudgetService_AProjectBudgetMayNameItsOwnProjectConsumer(t *testing.T) {
	env := newBudgetTestEnv(t)
	req := projectBudgetRequest("api-platform-total")
	req.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("api-platform")}}
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
	require.Len(t, env.created[0].Configuration.Consumers, 1, "not added twice")
}

func TestBudgetService_OwnerRefusals(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*spec.BudgetRequest)
		code  string
		field string
	}{
		{"another organization", func(r *spec.BudgetRequest) { r.OrgId = "acme" }, utils.ErrCodeValidation, "orgId"},
		{"no organization", func(r *spec.BudgetRequest) { r.OrgId = "" }, utils.ErrCodeValidation, "orgId"},
		{"unknown project", func(r *spec.BudgetRequest) { r.ProjectId = strPtr("nowhere") }, utils.ErrCodeReferenceNotFound, "projectId"},
		{"another project's consumer", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("support")}}
		}, utils.ErrCodeValidation, "consumers[0]"},
		{"each project", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Allocation: strPtr("separate")}}
		}, utils.ErrCodeValidation, "consumers[0]"},
		{"exempting a project", func(r *spec.BudgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "project", Id: strPtr("api-platform")}}
		}, utils.ErrCodeValidation, "exemptions[0].type"},
		{"exempting another project's agent", func(r *spec.BudgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("support"), Id: strPtr("triage-bot")}}
		}, utils.ErrCodeValidation, "exemptions[0].project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			req := projectBudgetRequest("x")
			tc.edit(&req)
			_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", req)
			var be *BudgetError
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tc.code, be.Code)
			assert.Equal(t, tc.field, be.Field)
			assert.Empty(t, env.created, "nothing is stored")
		})
	}
}

func TestBudgetService_OrgIDIsTheTokensOrganization(t *testing.T) {
	env := newBudgetTestEnv(t)
	ctx := orgctx.WithResolvedOrg(context.Background(), orgctx.ResolvedOrg{OUID: budgetTestOrg, OuHandle: "acme"})
	req := budgetTestRequest()
	req.OrgId = "acme"
	resp, err := env.svc.Create(ctx, budgetTestOrg, "alice", req)
	require.NoError(t, err, "the token's organization handle is used when the request has one")
	assert.Equal(t, "acme", resp.OrgId)

	req.Name, req.OrgId = "other", budgetTestOrgName
	_, err = env.svc.Create(ctx, budgetTestOrg, "alice", req)
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}

func TestBudgetService_ListsAProjectsBudgets(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "org-wide")
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)

	all, err := env.svc.List(context.Background(), budgetTestOrg, "", nil, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int32(2), all.Total, "the organization sees every budget")

	mine, err := env.svc.List(context.Background(), budgetTestOrg, "api-platform", nil, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int32(1), mine.Total)
	assert.Equal(t, "api-platform-agents", mine.Budgets[0].Name)

	none, err := env.svc.List(context.Background(), budgetTestOrg, "support", nil, 20, 0)
	require.NoError(t, err)
	assert.Zero(t, none.Total)
}

func TestBudgetService_UpdateKeepsAProjectBudgetsExemptionsInItsProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)
	update := spec.BudgetUpdateRequest{
		DisplayName: strPtr("API Platform agents"),
		Limit:       spec.BudgetUpdateLimit{Amount: "150", Action: "block"},
		Exemptions:  []spec.BudgetExemption{{Type: "agent", Project: strPtr("api-platform"), Id: strPtr("hotel-booking")}},
	}
	resp, err := env.svc.Update(context.Background(), budgetTestOrg, "api-platform-agents", update)
	require.NoError(t, err)
	require.NotNil(t, resp.ProjectId)
	assert.Equal(t, "api-platform", *resp.ProjectId)
	assert.Len(t, resp.Consumers, 2, "the implied project consumer is kept, not added again")

	update.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("support"), Id: strPtr("triage-bot")}}
	_, err = env.svc.Update(context.Background(), budgetTestOrg, "api-platform-agents", update)
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}

func TestGatewayResourceSync_TheProjectAnnotationMakesAProjectBudget(t *testing.T) {
	gw := &models.Gateway{UUID: resourceSyncGateway, OUID: resourceSyncOrg}
	var saved []models.Budget
	repo := &repomocks.BudgetRepositoryMock{
		SaveForGatewayFunc: func(_ context.Context, b *models.Budget, name string) (bool, error) {
			b.ID, b.Name = uuid.New(), name
			saved = append(saved, *b)
			return true, nil
		},
	}
	platform := &repomocks.PlatformRepositoryMock{
		GetProjectFunc: func(_ context.Context, ouID, name string) (*models.ProjectResponse, error) {
			assert.Equal(t, resourceSyncOrg, ouID)
			if name != "api-platform" {
				return nil, utils.ErrProjectNotFound
			}
			return &models.ProjectResponse{UUID: budgetTestProjectID, Name: name}, nil
		},
	}
	annotated := func(id, name, project string, consumers ...map[string]any) models.GatewayResourceItem {
		doc, _ := json.Marshal(map[string]any{
			"apiVersion": "gateway.api-platform.wso2.com/v1", "kind": "Budget",
			"metadata": map[string]any{
				"name": name, "annotations": map[string]string{AnnotationProjectID: project},
			},
			"spec": map[string]any{"consumers": consumers},
		})
		now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
		return models.GatewayResourceItem{DPID: id, Configuration: doc, CreatedAt: now, UpdatedAt: now}
	}
	svc := NewGatewayResourceSyncService(gatewayRepoFor(gw, nil), repo, platform)

	resp, err := svc.Sync(context.Background(), resourceSyncGateway.String(), resourceSyncOrg, models.GatewayResourcesRequest{
		Resources: []models.GatewayResourceItem{
			annotated("dp-project", "api-platform-daily", "api-platform"),
			annotated("dp-unknown", "nowhere-daily", "nowhere"),
			annotated("dp-other", "other-daily", "api-platform", map[string]any{"type": "project", "id": "support"}),
			budgetItem("dp-org", "Budget", "org-daily"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, models.GatewayResourceStatusSynced, resp.Resources["dp-project"].Status)
	assert.Equal(t, GatewayResourceErrReferenceNotFound, resp.Resources["dp-unknown"].Error)
	assert.Equal(t, GatewayResourceErrInvalid, resp.Resources["dp-other"].Error, "a project budget counts only its project")
	assert.Equal(t, models.GatewayResourceStatusSynced, resp.Resources["dp-org"].Status)

	require.Len(t, saved, 2)
	byName := map[string]models.Budget{saved[0].Name: saved[0], saved[1].Name: saved[1]}
	project := byName["api-platform-daily"]
	require.NotNil(t, project.ProjectID)
	assert.Equal(t, budgetTestProjectID, project.ProjectID.String())
	require.Len(t, project.Configuration.Consumers, 1)
	assert.Equal(t, "api-platform", *project.Configuration.Consumers[0].ID)
	assert.Nil(t, byName["org-daily"].ProjectID, "no annotation: an organization budget")
}

func TestInfraResourceManager_DeletingAProjectTellsGatewaysItsBudgetsAreGone(t *testing.T) {
	const org, proj = "acme", "api-platform"
	sender := &recordingSender{}
	budgets := &repomocks.BudgetRepositoryMock{
		ListFunc: func(context.Context, string) ([]models.Budget, error) {
			return []models.Budget{
				{Name: "org-wide", Origin: models.BudgetOriginControlPlane},
				{Name: "api-platform-agents", ProjectName: proj, Origin: models.BudgetOriginControlPlane},
				{Name: "from-gateway", ProjectName: proj, Origin: models.BudgetOriginGateway},
				{Name: "support-agents", ProjectName: "support", Origin: models.BudgetOriginControlPlane},
			}, nil
		},
	}
	deleted := false
	platform := &repomocks.PlatformRepositoryMock{
		GetOrganizationFunc:    okOrg(),
		CountProjectAgentsFunc: func(context.Context, string, string) (int, error) { return 0, nil },
		DeleteProjectFunc: func(context.Context, string, string) error {
			deleted = true
			return nil
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, NewInfraResourceManager(platform, budgets, sender, logger).DeleteProject(auditableCtx(t), org, proj))
	assert.True(t, deleted)
	assert.Equal(t, []string{"deleted:api-platform-agents"}, sender.sent,
		"only its control-plane budgets: a gateway's own budget becomes the organization's")

	sender.sent = nil
	platform.DeleteProjectFunc = func(context.Context, string, string) error { return utils.ErrConflict }
	require.Error(t, NewInfraResourceManager(platform, budgets, sender, logger).DeleteProject(auditableCtx(t), org, proj))
	assert.Empty(t, sender.sent, "nothing is sent when the project stays")
}
