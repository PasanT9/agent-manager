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
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

const budgetTestOrg = "0193f1c2-0000-7000-8000-000000000001"

var (
	budgetOrgChannel     = "3f2b8c1e-5a7d-4e9b-8c21-6d0f4a9e7b13"
	budgetProjectChannel = "9c4e1a7b-2d8f-4b3a-a5e6-1f7c0d2b8e94"
)

func budgetI32(v int32) *int32 { return &v }

// budgetTestRequest is the design's example payload (Interfaces 2.2).
func budgetTestRequest() spec.BudgetRequest {
	return spec.BudgetRequest{
		Name:         "api-platform-per-agent-openai",
		DisplayName:  "API Platform, per agent on OpenAI",
		Description:  strPtr("Every agent in API Platform gets $100 a month on OpenAI."),
		Limit:        spec.BudgetLimit{Amount: "100.00", Period: "month", Action: "block"},
		Resource:     &spec.BudgetResource{Type: "llmProvider", Id: strPtr("openai")},
		Environments: &spec.BudgetEnvironments{Ids: []string{"default"}},
		Consumers: []spec.BudgetConsumer{
			{Type: "project", Id: strPtr("api-platform")},
			{Type: "agent", Allocation: strPtr("separate")},
		},
		Exemptions: []spec.BudgetExemption{{Type: "agent", Project: strPtr("api-platform"), Id: strPtr("hotel-booking")}},
		Warnings:   &spec.BudgetWarnings{Thresholds: []int32{80, 100}, ChannelIds: []string{budgetOrgChannel, budgetProjectChannel}},
	}
}

type budgetTestEnv struct {
	svc     BudgetService
	budgets *repomocks.BudgetRepositoryMock
	created []models.Budget
	store   map[string]models.Budget
}

// newBudgetTestEnv has: provider openai, proxy support-proxy, project api-platform with
// agent hotel-booking, the default environment, an org-wide channel and a channel for
// project api-platform only.
func newBudgetTestEnv(t *testing.T) *budgetTestEnv {
	t.Helper()
	env := &budgetTestEnv{store: map[string]models.Budget{}}
	env.budgets = &repomocks.BudgetRepositoryMock{
		CreateForControlPlaneFunc: func(_ context.Context, b *models.Budget, name string) error {
			if name == "taken" {
				return utils.ErrBudgetAlreadyExists
			}
			b.ID, b.Name = uuid.New(), name
			b.CreatedAt = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
			b.UpdatedAt = b.CreatedAt
			env.created = append(env.created, *b)
			env.store[name] = *b
			return nil
		},
		GetFunc: func(_ context.Context, _, name string) (*models.Budget, error) {
			b, ok := env.store[name]
			if !ok {
				return nil, utils.ErrBudgetNotFound
			}
			return &b, nil
		},
		UpdateFunc: func(_ context.Context, b *models.Budget) error {
			b.UpdatedAt = b.CreatedAt.Add(time.Hour)
			env.store[b.Name] = *b
			return nil
		},
		DeleteFunc: func(_ context.Context, _, name string) error {
			delete(env.store, name)
			return nil
		},
		ListFunc: func(context.Context, string) ([]models.Budget, error) {
			out := []models.Budget{}
			for _, n := range sortedKinds(env.store) {
				out = append(out, env.store[n])
			}
			return out, nil
		},
	}
	artifacts := &repomocks.ArtifactRepositoryMock{
		ExistsFunc: func(kind, handle, _ string) (bool, error) {
			return (kind == models.KindLLMProvider && handle == "openai") ||
				(kind == models.KindLLMProxy && handle == "support-proxy"), nil
		},
	}
	platform := &repomocks.PlatformRepositoryMock{
		GetProjectFunc: func(_ context.Context, _, name string) (*models.ProjectResponse, error) {
			if name != "api-platform" {
				return nil, utils.ErrProjectNotFound
			}
			return &models.ProjectResponse{Name: name}, nil
		},
		GetAgentFunc: func(_ context.Context, _, project, name string) (*models.AgentResponse, error) {
			if project != "api-platform" || name != "hotel-booking" {
				return nil, utils.ErrAgentNotFound
			}
			return &models.AgentResponse{Name: name}, nil
		},
		GetEnvironmentFunc: func(context.Context, string, string) (*models.EnvironmentResponse, error) {
			return &models.EnvironmentResponse{Name: models.DefaultEnvironmentName}, nil
		},
	}
	events := &repomocks.EventRepositoryMock{
		GetDestinationFunc: func(_ context.Context, ouID string, id uuid.UUID) (*models.EventDestination, error) {
			switch id.String() {
			case budgetOrgChannel:
				return &models.EventDestination{ID: id, OUID: ouID, Availability: models.DestinationAvailableOrg}, nil
			case budgetProjectChannel:
				return &models.EventDestination{
					ID: id, OUID: ouID, Availability: models.DestinationAvailableProjects, Projects: []string{"api-platform"},
				}, nil
			}
			return nil, gorm.ErrRecordNotFound
		},
	}
	env.svc = NewBudgetService(env.budgets, artifacts, platform, events)
	return env
}

func TestBudgetService_CreateStoresTheBudgetKindWithDefaults(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)

	require.Len(t, env.created, 1)
	b := env.created[0]
	assert.Equal(t, budgetTestOrg, b.OrgID)
	assert.Equal(t, models.BudgetOriginControlPlane, b.Origin)
	assert.Nil(t, b.GatewayID)
	assert.Equal(t, "alice", b.CreatedBy)
	stored, err := json.Marshal(b.Configuration)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"displayName": "API Platform, per agent on OpenAI",
		"description": "Every agent in API Platform gets $100 a month on OpenAI.",
		"status": "active",
		"limit": {"amount": "100.00", "currency": "USD", "period": "month", "action": "block", "blockAt": 100},
		"resource": {"type": "llmProvider", "allocation": "shared", "id": "openai"},
		"environments": {"allocation": "shared", "ids": ["default"]},
		"consumers": [
			{"type": "project", "allocation": "shared", "id": "api-platform"},
			{"type": "agent", "allocation": "separate"}
		],
		"exemptions": [{"type": "agent", "project": "api-platform", "id": "hotel-booking"}],
		"warnings": {"thresholds": [80, 100], "channelIds": ["`+budgetOrgChannel+`", "`+budgetProjectChannel+`"]}
	}`, string(stored), "the kind's spec, without apiVersion, kind and metadata")

	assert.Equal(t, "api-platform-per-agent-openai", resp.Name)
	assert.Equal(t, "active", resp.Status)
	assert.Equal(t, "control_plane", resp.Origin)
	assert.Equal(t, "USD", *resp.Limit.Currency)
	assert.Equal(t, int32(100), *resp.Limit.BlockAt)
	assert.Equal(t, "alice", *resp.CreatedBy)
	assert.Equal(t, b.CreatedAt, resp.CreatedAt)
	out, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "currentPeriod", "spend is not reported until usage reports exist")
}

func TestBudgetService_CreateAllowsAMinimalBudget(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "", spec.BudgetRequest{
		Name: "org-monthly", DisplayName: "Org monthly",
		Limit: spec.BudgetLimit{Amount: "5000", Period: "month", Action: "allow"},
	})
	require.NoError(t, err)
	assert.Nil(t, resp.Limit.BlockAt, "blockAt is for block only")
	assert.Empty(t, resp.Consumers)
	assert.NotNil(t, resp.Consumers, "consumers is always a list")
	assert.Nil(t, resp.CreatedBy)
}

func TestBudgetService_CreateRefusesInvalidPayloads(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*spec.BudgetRequest)
		code  string
		field string
	}{
		{"bad name", func(r *spec.BudgetRequest) { r.Name = "Team A" }, utils.ErrCodeValidation, "name"},
		{"blank display name", func(r *spec.BudgetRequest) { r.DisplayName = "  " }, utils.ErrCodeValidation, "displayName"},
		{"zero amount", func(r *spec.BudgetRequest) { r.Limit.Amount = "0.00" }, utils.ErrCodeValidation, "limit.amount"},
		{"three decimals", func(r *spec.BudgetRequest) { r.Limit.Amount = "1.005" }, utils.ErrCodeValidation, "limit.amount"},
		{"other currency", func(r *spec.BudgetRequest) { r.Limit.Currency = strPtr("EUR") }, utils.ErrCodeValidation, "limit.currency"},
		{"bad period", func(r *spec.BudgetRequest) { r.Limit.Period = "year" }, utils.ErrCodeValidation, "limit.period"},
		{"bad action", func(r *spec.BudgetRequest) { r.Limit.Action = "warn" }, utils.ErrCodeValidation, "limit.action"},
		{"blockAt out of range", func(r *spec.BudgetRequest) { r.Limit.BlockAt = budgetI32(101) }, utils.ErrCodeValidation, "limit.blockAt"},
		{"blockAt with allow", func(r *spec.BudgetRequest) {
			r.Limit.Action, r.Limit.BlockAt = "allow", budgetI32(90)
		}, utils.ErrCodeValidation, "limit.blockAt"},
		{"separate resource with id", func(r *spec.BudgetRequest) {
			r.Resource.Allocation = strPtr("separate")
		}, utils.ErrCodeValidation, "resource.id"},
		{"duplicate environment", func(r *spec.BudgetRequest) {
			r.Environments.Ids = []string{"default", "default"}
		}, utils.ErrCodeValidation, "environments.ids[1]"},
		{"consumer type twice", func(r *spec.BudgetRequest) {
			r.Consumers = append(r.Consumers, spec.BudgetConsumer{Type: "agent", Allocation: strPtr("separate")})
		}, utils.ErrCodeValidation, "consumers[2].type"},
		{"shared consumer without id", func(r *spec.BudgetRequest) {
			r.Consumers[0].Id = nil
		}, utils.ErrCodeValidation, "consumers[0].id"},
		{"named agent without project", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "agent", Id: strPtr("hotel-booking")}}
		}, utils.ErrCodeValidation, "consumers[0].id"},
		{"attribute without expression", func(r *spec.BudgetRequest) {
			r.Consumers = append(r.Consumers, spec.BudgetConsumer{Type: "attribute"})
		}, utils.ErrCodeValidation, "consumers[2].expression"},
		{"agent exemption without project", func(r *spec.BudgetRequest) {
			r.Exemptions[0].Project = nil
		}, utils.ErrCodeValidation, "exemptions[0]"},
		{"threshold out of range", func(r *spec.BudgetRequest) {
			r.Warnings.Thresholds = []int32{1001}
		}, utils.ErrCodeValidation, "warnings.thresholds[0]"},
		{"bad channel id", func(r *spec.BudgetRequest) {
			r.Warnings.ChannelIds = []string{"not-a-uuid"}
		}, utils.ErrCodeValidation, "warnings.channelIds[0]"},
		{"unknown provider", func(r *spec.BudgetRequest) { r.Resource.Id = strPtr("anthropic") }, utils.ErrCodeReferenceNotFound, "resource.id"},
		{"unknown environment", func(r *spec.BudgetRequest) {
			r.Environments.Ids = []string{"production"}
		}, utils.ErrCodeReferenceNotFound, "environments.ids[0]"},
		{"unknown project", func(r *spec.BudgetRequest) { r.Consumers[0].Id = strPtr("other") }, utils.ErrCodeReferenceNotFound, "consumers[0].id"},
		{"unknown proxy", func(r *spec.BudgetRequest) {
			r.Consumers = append(r.Consumers, spec.BudgetConsumer{Type: "llmProxy", Id: strPtr("missing-proxy")})
		}, utils.ErrCodeReferenceNotFound, "consumers[2].id"},
		{"unknown exempt agent", func(r *spec.BudgetRequest) {
			r.Exemptions[0].Id = strPtr("other-agent")
		}, utils.ErrCodeReferenceNotFound, "exemptions[0].id"},
		{"unknown channel", func(r *spec.BudgetRequest) {
			r.Warnings.ChannelIds = []string{uuid.NewString()}
		}, utils.ErrCodeChannelNotAvailable, "warnings.channelIds[0]"},
		{"project channel without that project", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Allocation: strPtr("separate")}}
			r.Exemptions = nil
		}, utils.ErrCodeChannelNotAvailable, "warnings.channelIds[1]"},
		{"name taken", func(r *spec.BudgetRequest) { r.Name = "taken" }, utils.ErrCodeBudgetAlreadyExists, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			req := budgetTestRequest()
			tt.edit(&req)
			_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", req)
			var be *BudgetError
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tt.code, be.Code)
			assert.Equal(t, tt.field, be.Field)
			if tt.code != utils.ErrCodeBudgetAlreadyExists {
				assert.Empty(t, env.created, "nothing is stored")
			}
		})
	}
}

func TestBudgetService_CreateKeepsRealErrors(t *testing.T) {
	env := newBudgetTestEnv(t)
	dbDown := errors.New("db down")
	env.budgets.CreateForControlPlaneFunc = func(context.Context, *models.Budget, string) error { return dbDown }
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	assert.ErrorIs(t, err, dbDown)
	var be *BudgetError
	assert.False(t, errors.As(err, &be), "a database failure is not a refused request")

	_, err = env.svc.Create(context.Background(), "", "alice", budgetTestRequest())
	assert.ErrorIs(t, err, utils.ErrOrganizationNotFound)
}

func TestBuildBudgetKind(t *testing.T) {
	env := newBudgetTestEnv(t)
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)
	cfg := env.created[0].Configuration

	full, err := json.Marshal(BuildBudgetKind("api-platform-per-agent-openai", cfg, false))
	require.NoError(t, err)
	assert.Contains(t, string(full), `"apiVersion":"gateway.api-platform.wso2.com/v1","kind":"Budget","metadata":{"name":"api-platform-per-agent-openai"}`)
	assert.Contains(t, string(full), `"displayName":"API Platform, per agent on OpenAI"`)
	assert.Contains(t, string(full), `"warnings"`)

	gw, err := json.Marshal(BuildBudgetKind("api-platform-per-agent-openai", cfg, true))
	require.NoError(t, err)
	for _, absent := range []string{"displayName", "description", "warnings"} {
		assert.NotContains(t, string(gw), `"`+absent+`"`, "gateways never get %s", absent)
	}
	assert.Equal(t, "API Platform, per agent on OpenAI", cfg.DisplayName, "the stored budget is not changed")
}

func TestGatewayBudgetConfigMapsTheKindsSpec(t *testing.T) {
	cfg, err := gatewayBudgetConfig("research-monthly", json.RawMessage(`{
		"apiVersion": "gateway.api-platform.wso2.com/v1", "kind": "Budget",
		"metadata": {"name": "research-monthly"},
		"spec": {
			"limit": {"amount": "500.00", "period": "month", "action": "block", "blockAt": 95},
			"resource": {"type": "llmProvider", "id": "openai"},
			"consumers": [{"type": "attribute", "expression": "'research' in request.Auth.Claims[\"groups\"]"}]
		}
	}`))
	require.NoError(t, err)
	assert.Equal(t, "research-monthly", cfg.DisplayName, "a gateway budget shows its name")
	assert.Equal(t, "active", cfg.Status)
	assert.Equal(t, "USD", cfg.Limit.Currency)
	assert.Equal(t, int32(95), *cfg.Limit.BlockAt)
	assert.Equal(t, "shared", cfg.Resource.Allocation)
	assert.Equal(t, "shared", cfg.Consumers[0].Allocation)
	assert.NotNil(t, cfg.Exemptions)

	_, err = gatewayBudgetConfig("x", json.RawMessage(`not json`))
	assert.Error(t, err)
}

// createTestBudget creates the design's example budget, named name.
func createTestBudget(t *testing.T, env *budgetTestEnv, name string) {
	t.Helper()
	req := budgetTestRequest()
	req.Name = name
	_, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
}

func budgetErrorCode(t *testing.T, err error) string {
	t.Helper()
	var be *BudgetError
	require.ErrorAs(t, err, &be)
	return be.Code
}

func TestBudgetService_GetReturnsTheBudgetOrNotFound(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	resp, err := env.svc.Get(context.Background(), budgetTestOrg, "team-a")
	require.NoError(t, err)
	assert.Equal(t, "team-a", resp.Name)
	assert.Equal(t, "control_plane", resp.Origin)

	_, err = env.svc.Get(context.Background(), budgetTestOrg, "missing")
	assert.Equal(t, utils.ErrCodeBudgetNotFound, budgetErrorCode(t, err))
}

func TestBudgetService_ListFiltersAndPages(t *testing.T) {
	env := newBudgetTestEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		createTestBudget(t, env, n)
	}
	other := env.store["c"]
	other.Configuration.Resource = nil
	other.Configuration.Consumers = []models.BudgetConsumer{}
	other.Configuration.Exemptions = []models.BudgetExemption{}
	env.store["c"] = other

	page, err := env.svc.List(context.Background(), budgetTestOrg, nil, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, int32(3), page.Total)
	assert.Equal(t, int32(2), page.Limit)
	require.Len(t, page.Budgets, 2)
	assert.Equal(t, "a", page.Budgets[0].Name)

	page, err = env.svc.List(context.Background(), budgetTestOrg, nil, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int32(20), page.Limit, "default page size")
	require.Len(t, page.Budgets, 1)
	assert.Equal(t, "c", page.Budgets[0].Name)

	page, err = env.svc.List(context.Background(), budgetTestOrg, nil, 20, 10)
	require.NoError(t, err)
	assert.Empty(t, page.Budgets)
	assert.Equal(t, int32(3), page.Total)

	for filter, want := range map[string]int{
		"llmProvider:openai":   2,
		"project:api-platform": 2,
		"agent:hotel-booking":  2,
		"llmProvider:other":    0,
	} {
		page, err = env.svc.List(context.Background(), budgetTestOrg, []string{filter}, 20, 0)
		require.NoError(t, err)
		assert.Len(t, page.Budgets, want, filter)
	}
	page, err = env.svc.List(context.Background(), budgetTestOrg, []string{"project:api-platform", "llmProvider:other"}, 20, 0)
	require.NoError(t, err)
	assert.Empty(t, page.Budgets, "filters combine with AND")

	for _, bad := range [][]string{{"team:a"}, {"project"}, {"project:"}} {
		_, err = env.svc.List(context.Background(), budgetTestOrg, bad, 20, 0)
		assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err), bad)
	}
	_, err = env.svc.List(context.Background(), budgetTestOrg, nil, 101, 0)
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}

func TestBudgetService_UpdateReplacesTheEditableFields(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	inactive := "inactive"
	resp, err := env.svc.Update(context.Background(), budgetTestOrg, "team-a", spec.BudgetUpdateRequest{
		DisplayName: "Team A",
		Status:      &inactive,
		Limit:       spec.BudgetUpdateLimit{Amount: "150.00", Action: "block", BlockAt: budgetI32(90)},
		Warnings:    &spec.BudgetWarnings{Thresholds: []int32{100}, ChannelIds: []string{budgetOrgChannel}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Team A", resp.DisplayName)
	assert.Equal(t, "inactive", resp.Status)
	assert.Nil(t, resp.Description, "omitted fields are replaced, not kept")
	assert.Empty(t, resp.Exemptions)
	assert.Equal(t, "150.00", resp.Limit.Amount)
	assert.Equal(t, int32(90), *resp.Limit.BlockAt)
	assert.Equal(t, "month", resp.Limit.Period, "period is fixed")
	assert.Equal(t, "USD", *resp.Limit.Currency, "currency is fixed")
	assert.Len(t, resp.Consumers, 2, "consumers are fixed")
	assert.Equal(t, "openai", *resp.Resource.Id, "resource is fixed")
	assert.Equal(t, "inactive", env.store["team-a"].Configuration.Status)

	resp, err = env.svc.Update(context.Background(), budgetTestOrg, "team-a", spec.BudgetUpdateRequest{
		DisplayName: "Team A", Limit: spec.BudgetUpdateLimit{Amount: "150.00", Action: "allow"},
	})
	require.NoError(t, err)
	assert.Equal(t, "active", resp.Status, "status defaults to active")
	assert.Nil(t, resp.Limit.BlockAt)
}

func TestBudgetService_UpdateRefusals(t *testing.T) {
	ok := func() spec.BudgetUpdateRequest {
		return spec.BudgetUpdateRequest{DisplayName: "Team A", Limit: spec.BudgetUpdateLimit{Amount: "150.00", Action: "block"}}
	}
	tests := []struct {
		name   string
		budget string
		edit   func(*spec.BudgetUpdateRequest)
		code   string
	}{
		{"not found", "missing", func(*spec.BudgetUpdateRequest) {}, utils.ErrCodeBudgetNotFound},
		{"gateway budget", "from-gateway", func(*spec.BudgetUpdateRequest) {}, utils.ErrCodeBudgetReadOnly},
		{"bad status", "team-a", func(r *spec.BudgetUpdateRequest) { r.Status = strPtr("paused") }, utils.ErrCodeValidation},
		{"bad amount", "team-a", func(r *spec.BudgetUpdateRequest) { r.Limit.Amount = "0" }, utils.ErrCodeValidation},
		{"blockAt with allow", "team-a", func(r *spec.BudgetUpdateRequest) {
			r.Limit.Action, r.Limit.BlockAt = "allow", budgetI32(50)
		}, utils.ErrCodeValidation},
		{"unknown exempt agent", "team-a", func(r *spec.BudgetUpdateRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("api-platform"), Id: strPtr("nope")}}
		}, utils.ErrCodeReferenceNotFound},
		{"unknown channel", "team-a", func(r *spec.BudgetUpdateRequest) {
			r.Warnings = &spec.BudgetWarnings{Thresholds: []int32{80}, ChannelIds: []string{uuid.NewString()}}
		}, utils.ErrCodeChannelNotAvailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			createTestBudget(t, env, "team-a")
			gw := env.store["team-a"]
			gw.Name, gw.Origin = "from-gateway", models.BudgetOriginGateway
			env.store["from-gateway"] = gw
			before := env.store["team-a"].Configuration

			req := ok()
			tt.edit(&req)
			_, err := env.svc.Update(context.Background(), budgetTestOrg, tt.budget, req)
			assert.Equal(t, tt.code, budgetErrorCode(t, err))
			assert.Equal(t, before, env.store["team-a"].Configuration, "nothing is stored")
			assert.Empty(t, env.budgets.UpdateCalls())
		})
	}
}

func TestBudgetService_DeleteRemovesControlPlaneBudgetsOnly(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	gw := env.store["team-a"]
	gw.Name, gw.Origin = "from-gateway", models.BudgetOriginGateway
	env.store["from-gateway"] = gw

	require.NoError(t, env.svc.Delete(auditableCtx(t), budgetTestOrg, "team-a"))
	assert.NotContains(t, env.store, "team-a")

	err := env.svc.Delete(auditableCtx(t), budgetTestOrg, "team-a")
	assert.Equal(t, utils.ErrCodeBudgetNotFound, budgetErrorCode(t, err))
	err = env.svc.Delete(auditableCtx(t), budgetTestOrg, "from-gateway")
	assert.Equal(t, utils.ErrCodeBudgetReadOnly, budgetErrorCode(t, err))
	assert.Contains(t, env.store, "from-gateway")

	dbDown := errors.New("db down")
	env.budgets.GetFunc = func(context.Context, string, string) (*models.Budget, error) { return nil, dbDown }
	assert.ErrorIs(t, env.svc.Delete(auditableCtx(t), budgetTestOrg, "x"), dbDown, "a real error is not masked as not found")
}
