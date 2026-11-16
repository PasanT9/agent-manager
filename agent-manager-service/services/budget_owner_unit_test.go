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
func projectBudgetRequest(name string) budgetRequest {
	return budgetRequest{
		Name: name, ProjectId: strPtr("api-platform"), DisplayName: strPtr(name),
		Limit: spec.BudgetLimit{Amount: "100", Period: "month", Action: "block"},
		Consumers: []spec.BudgetConsumer{
			{Type: "project", Allocation: strPtr("shared"), Id: strPtr("api-platform")},
			{Type: "agent", Allocation: strPtr("separate")},
		},
	}
}

func TestBudgetService_AProjectBudgetCountsOnlyItsProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)

	require.Len(t, env.created, 1)
	b := env.created[0]
	require.NotNil(t, b.ProjectID)
	assert.Equal(t, budgetTestProjectID, b.ProjectID.String(), "the owning project is stored by ID")
	require.Len(t, b.Configuration.Consumers, 2, "the consumers as sent: nothing is added")
	project := b.Configuration.Consumers[0]
	assert.Equal(t, "project", project.Type)
	assert.Equal(t, "shared", project.Allocation)
	assert.Equal(t, "api-platform", *project.ID)
	assert.Equal(t, "agent", b.Configuration.Consumers[1].Type)

	require.NotNil(t, kindProject(resp))
	assert.Equal(t, "api-platform", *kindProject(resp))

	kind := BuildBudgetKind(b.Name, b.Configuration, true)
	out, err := json.Marshal(kind)
	require.NoError(t, err)
	assert.Contains(t, string(out), `{"type":"project","allocation":"shared","id":"api-platform"}`,
		"gateways get the project as a consumer")
}

func TestBudgetService_AnOrganizationBudgetHasNoProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)
	assert.Nil(t, env.created[0].ProjectID)
	assert.Nil(t, kindProject(resp))
	out, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "projectId")
}

func TestBudgetService_AProjectBudgetMayNameItsOwnProjectConsumer(t *testing.T) {
	env := newBudgetTestEnv(t)
	req := projectBudgetRequest("api-platform-total")
	req.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("api-platform")}}
	_, err := env.create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
	require.Len(t, env.created[0].Configuration.Consumers, 1, "not added twice")
}

func TestBudgetService_AProjectBudgetGetsItsProjectConsumer(t *testing.T) {
	env := newBudgetTestEnv(t)
	ctx := context.Background()
	sent := projectBudgetRequest("api-platform-agents")
	sent.Consumers = sent.Consumers[1:] // only the agent consumer
	resp, err := env.create(ctx, budgetTestOrg, "alice", sent)
	require.NoError(t, err)

	stored := env.created[0].Configuration.Consumers
	require.Len(t, stored, 2)
	assert.Equal(t, "project", stored[0].Type, "added first")
	assert.Equal(t, "shared", stored[0].Allocation)
	assert.Equal(t, "api-platform", *stored[0].ID)
	require.Len(t, resp.Spec.Consumers, 2, "the response lists it")
	assert.Equal(t, "project", resp.Spec.Consumers[0].Type)

	// The body first sent, without the project consumer, can be sent again as an update.
	sent.Limit.Amount = "150"
	updated, err := env.svc.Update(ctx, budgetTestOrg, sent.Name, asKind(sent))
	require.NoError(t, err)
	assert.Equal(t, "150", updated.Spec.Limit.Amount)
}

func TestBudgetService_OwnerRefusals(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*budgetRequest)
		code  string
		field string
	}{
		{"an empty project annotation", func(r *budgetRequest) { r.ProjectId = strPtr(" ") }, utils.ErrCodeValidation, "metadata.annotations"},
		{"unknown project", func(r *budgetRequest) { r.ProjectId = strPtr("nowhere") }, utils.ErrCodeReferenceNotFound, "metadata.annotations"},
		{"another project's consumer", func(r *budgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("support")}}
		}, utils.ErrCodeValidation, "consumers[0]"},
		{"each project", func(r *budgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Allocation: strPtr("separate")}}
		}, utils.ErrCodeValidation, "consumers[0]"},
		{"exempting a project", func(r *budgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "project", Id: strPtr("api-platform")}}
		}, utils.ErrCodeValidation, "exemptions[0].type"},
		{"exempting another project's agent", func(r *budgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("support"), Id: strPtr("triage-bot")}}
		}, utils.ErrCodeValidation, "exemptions[0].project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			req := projectBudgetRequest("x")
			tc.edit(&req)
			_, err := env.create(context.Background(), budgetTestOrg, "alice", req)
			var be *BudgetError
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tc.code, be.Code)
			assert.Equal(t, tc.field, be.Field)
			assert.Empty(t, env.created, "nothing is stored")
		})
	}
}

func TestBudgetService_TheOrganizationIsTheOneInThePath(t *testing.T) {
	env := newBudgetTestEnv(t)
	ctx := orgctx.WithResolvedOrg(context.Background(), orgctx.ResolvedOrg{OUID: budgetTestOrg, OuHandle: "acme"})
	_, err := env.create(ctx, budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)
	assert.Equal(t, "acme", env.created[0].OrgName, "the kind has no organization: it is the token's")
}

func TestBudgetService_RefusesWhatIsNotABudgetKind(t *testing.T) {
	env := newBudgetTestEnv(t)
	for _, tc := range []struct {
		name, field string
		edit        func(*spec.Budget)
	}{
		{"another apiVersion", "apiVersion", func(k *spec.Budget) { k.ApiVersion = "v2" }},
		{"another kind", "kind", func(k *spec.Budget) { k.Kind = "LlmProvider" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := asKind(budgetTestRequest())
			tc.edit(&kind)
			_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", kind)
			var be *BudgetError
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tc.field, be.Field)
		})
	}
}

func TestBudgetService_ListsAProjectsBudgets(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "org-wide")
	_, err := env.create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)

	all, err := env.svc.List(context.Background(), budgetTestOrg, "", nil, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int32(2), all.Total, "the organization sees every budget")

	mine, err := env.svc.List(context.Background(), budgetTestOrg, "api-platform", nil, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int32(1), mine.Total)
	assert.Equal(t, "api-platform-agents", mine.Budgets[0].Metadata.Name)

	none, err := env.svc.List(context.Background(), budgetTestOrg, "support", nil, 20, 0)
	require.NoError(t, err)
	assert.Zero(t, none.Total)
}

func TestBudgetService_UpdateKeepsAProjectBudgetsExemptionsInItsProject(t *testing.T) {
	env := newBudgetTestEnv(t)
	_, err := env.create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)
	update := budgetUpdateRequest{
		DisplayName: strPtr("API Platform agents"),
		Limit:       budgetUpdateLimit{Amount: "150", Action: "block"},
		Exemptions:  []spec.BudgetExemption{{Type: "agent", Project: strPtr("api-platform"), Id: strPtr("hotel-booking")}},
	}
	resp, err := env.update(context.Background(), budgetTestOrg, "api-platform-agents", update)
	require.NoError(t, err)
	require.NotNil(t, kindProject(resp))
	assert.Equal(t, "api-platform", *kindProject(resp))
	assert.Len(t, resp.Spec.Consumers, 2, "the implied project consumer is kept, not added again")

	update.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("support"), Id: strPtr("triage-bot")}}
	_, err = env.update(context.Background(), budgetTestOrg, "api-platform-agents", update)
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
			annotated("dp-project", "api-platform-daily", "api-platform", map[string]any{"type": "project", "id": "api-platform"}),
			annotated("dp-no-consumer", "unscoped-daily", "api-platform"),
			annotated("dp-unknown", "nowhere-daily", "nowhere"),
			annotated("dp-other", "other-daily", "api-platform", map[string]any{"type": "project", "id": "support"}),
			budgetItem("dp-org", "Budget", "org-daily"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, models.GatewayResourceStatusSynced, resp.Resources["dp-project"].Status)
	assert.Equal(t, GatewayResourceErrInvalid, resp.Resources["dp-no-consumer"].Error, "a project budget lists its project consumer")
	assert.Equal(t, GatewayResourceErrReferenceNotFound, resp.Resources["dp-unknown"].Error)
	assert.Equal(t, GatewayResourceErrInvalid, resp.Resources["dp-other"].Error, "a project budget counts only its project")
	assert.Equal(t, models.GatewayResourceStatusSynced, resp.Resources["dp-org"].Status)

	require.Len(t, saved, 2)
	byName := map[string]models.Budget{saved[0].Name: saved[0], saved[1].Name: saved[1]}
	project := byName["api-platform-daily"]
	require.NotNil(t, project.ProjectID)
	assert.Equal(t, budgetTestProjectID, project.ProjectID.String())
	assert.Equal(t, "api-platform", project.ProjectName, "the stored kind keeps its project annotation")
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

func TestBudgetService_UpdateRefusesChangesToFixedFields(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		edit        func(*spec.Budget)
	}{
		{"rename", "metadata.name", func(k *spec.Budget) { k.Metadata.Name = "renamed" }},
		{"move to the organization", "metadata.annotations", func(k *spec.Budget) { k.Metadata.Annotations = nil }},
		{"period", "spec.limit.period", func(k *spec.Budget) { k.Spec.Limit.Period = "week" }},
		{"resource", "spec.resource", func(k *spec.Budget) {
			k.Spec.Resource = &spec.BudgetResource{Type: "llmProvider", Id: strPtr("anthropic")}
		}},
		{"consumers", "spec.consumers", func(k *spec.Budget) {
			k.Spec.Consumers = []spec.BudgetConsumer{{Type: "attribute", Expression: strPtr(budgetGroupExpr)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			created, err := env.create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
			require.NoError(t, err)
			kind := *created
			kind.Status = nil
			tc.edit(&kind)
			_, err = env.svc.Update(context.Background(), budgetTestOrg, "api-platform-agents", kind)
			var be *BudgetError
			require.ErrorAs(t, err, &be)
			assert.Equal(t, utils.ErrCodeValidation, be.Code)
			assert.Equal(t, tc.field, be.Field)
		})
	}
}

func TestBudgetService_TheReturnedKindCanBeSentBack(t *testing.T) {
	env := newBudgetTestEnv(t)
	created, err := env.create(context.Background(), budgetTestOrg, "alice", projectBudgetRequest("api-platform-agents"))
	require.NoError(t, err)
	assert.Equal(t, "api-platform", *kindProject(created))
	kind := *created
	kind.Spec.Limit.Amount = "150.00"
	updated, err := env.svc.Update(context.Background(), budgetTestOrg, "api-platform-agents", kind)
	require.NoError(t, err, "the implied project consumer and the defaults round-trip")
	assert.Equal(t, "150.00", updated.Spec.Limit.Amount)
}

func TestBudgetService_ABudgetCanBeCreatedInactive(t *testing.T) {
	env := newBudgetTestEnv(t)
	req := budgetTestRequest()
	req.Status = strPtr("inactive")
	resp, err := env.create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
	assert.Equal(t, "inactive", *resp.Spec.Status)
}
