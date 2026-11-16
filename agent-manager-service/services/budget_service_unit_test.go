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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// budgetTestNow is Thursday 12 November 2026: in the week from Monday 9 November.
var budgetTestNow = time.Date(2026, 11, 12, 15, 30, 0, 0, time.UTC)

const (
	budgetTestOrg       = "0193f1c2-0000-7000-8000-000000000001"
	budgetTestOrgName   = "wso2"
	budgetTestProjectID = "0193f1c2-0000-7000-8000-0000000000a1"
)

var (
	budgetOrgChannel     = "3f2b8c1e-5a7d-4e9b-8c21-6d0f4a9e7b13"
	budgetProjectChannel = "9c4e1a7b-2d8f-4b3a-a5e6-1f7c0d2b8e94"
)

func budgetI32(v int32) *int32 { return &v }

// Gateways identify consumers by token claims (attributes) today.
const (
	budgetGroupExpr = `'research' in request.Auth.Claims["groups"]`
	budgetEmailExpr = `request.Auth.Claims["email"]`
	budgetAdminExpr = `request.Auth.Claims["role"] == "admin"`
)

// recordingSender records the budgets sent to gateways.
type recordingSender struct{ sent []string }

func (r *recordingSender) SendBudget(_ context.Context, b *models.Budget, created bool) {
	event := "updated"
	if created {
		event = "created"
	}
	r.sent = append(r.sent, event+":"+b.Name+":"+b.Configuration.Status)
}

func (r *recordingSender) SendBudgetDeleted(_ context.Context, b *models.Budget) {
	r.sent = append(r.sent, "deleted:"+b.Name)
}

// budgetTestRequest is the design's example payload (Interfaces 2.2).
func budgetTestRequest() spec.BudgetRequest {
	return spec.BudgetRequest{
		Name:        "api-platform-per-agent-openai",
		OrgId:       budgetTestOrgName,
		DisplayName: strPtr("API Platform, per agent on OpenAI"),
		Description: strPtr("Every agent in API Platform gets $100 a month on OpenAI."),
		Limit:       spec.BudgetLimit{Amount: "100.00", Period: "month", Action: "block"},
		Resource:    &spec.BudgetResource{Type: "llmProvider", Id: strPtr("openai")},
		Consumers: []spec.BudgetConsumer{
			{Type: "attribute", Expression: strPtr(budgetGroupExpr)},
			{Type: "attribute", Allocation: strPtr("separate"), Expression: strPtr(budgetEmailExpr)},
		},
		Exemptions: []spec.BudgetExemption{{Type: "attribute", Expression: strPtr(budgetAdminExpr)}},
		Warnings:   &spec.BudgetWarnings{Thresholds: []int32{80, 100}, ChannelIds: []string{budgetOrgChannel}},
	}
}

type budgetTestEnv struct {
	svc     BudgetService
	usage   []models.BudgetUsage
	budgets *repomocks.BudgetRepositoryMock
	created []models.Budget
	store   map[string]models.Budget
	sender  *recordingSender
}

// newBudgetTestEnv has: provider openai, proxy support-proxy, project api-platform with
// agent hotel-booking, an org-wide channel and a channel for
// project api-platform only.
func newBudgetTestEnv(t *testing.T) *budgetTestEnv {
	t.Helper()
	env := &budgetTestEnv{store: map[string]models.Budget{}, sender: &recordingSender{}}
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
		CreateManyForControlPlaneFunc: func(_ context.Context, bs []*models.Budget, names []string) error {
			for i := range bs {
				if _, ok := env.store[names[i]]; ok {
					return &repositories.BudgetNameTakenError{Name: names[i]}
				}
			}
			for i, b := range bs {
				b.ID, b.Name = uuid.New(), names[i]
				b.CreatedAt = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
				b.UpdatedAt = b.CreatedAt
				env.created = append(env.created, *b)
				env.store[names[i]] = *b
			}
			return nil
		},
		TakenNamesFunc: func(_ context.Context, _ string, names []string) ([]string, error) {
			taken := []string{}
			for _, n := range names {
				if _, ok := env.store[n]; ok || n == "taken" || n == "openai" {
					taken = append(taken, n)
				}
			}
			return taken, nil
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
			return kind == models.KindLLMProvider && handle == "openai", nil
		},
	}
	platform := &repomocks.PlatformRepositoryMock{
		GetProjectFunc: func(_ context.Context, _, name string) (*models.ProjectResponse, error) {
			if name != "api-platform" {
				return nil, utils.ErrProjectNotFound
			}
			return &models.ProjectResponse{UUID: budgetTestProjectID, Name: name}, nil
		},
		GetOrganizationFunc: func(context.Context, string) (*models.OrganizationResponse, error) {
			return &models.OrganizationResponse{Name: budgetTestOrgName, Handle: budgetTestOrgName}, nil
		},
		GetAgentFunc: func(_ context.Context, _, project, name string) (*models.AgentResponse, error) {
			if project != "api-platform" || name != "hotel-booking" {
				return nil, utils.ErrAgentNotFound
			}
			return &models.AgentResponse{Name: name}, nil
		},
	}
	events := &repomocks.EventRepositoryMock{
		ListDestinationsFunc: func(_ context.Context, ouID string) ([]models.EventDestination, error) {
			return []models.EventDestination{
				{ID: uuid.MustParse(budgetOrgChannel), OUID: ouID, Name: "finance-email", Availability: models.DestinationAvailableOrg},
				{
					ID: uuid.MustParse(budgetProjectChannel), OUID: ouID, Name: "platform-slack",
					Availability: models.DestinationAvailableProjects, Projects: []string{"api-platform"},
				},
			}, nil
		},
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
	usageRepo := &repomocks.BudgetUsageRepositoryMock{
		ForBudgetFunc: func(_ context.Context, id uuid.UUID) ([]models.BudgetUsage, error) {
			out := []models.BudgetUsage{}
			for _, u := range env.usage {
				if u.BudgetID == id {
					out = append(out, u)
				}
			}
			return out, nil
		},
		ForBudgetsFunc: func(_ context.Context, _ string, ids []uuid.UUID) ([]models.BudgetUsage, error) {
			out := []models.BudgetUsage{}
			for _, u := range env.usage {
				for _, id := range ids {
					if u.BudgetID == id {
						out = append(out, u)
					}
				}
			}
			return out, nil
		},
	}
	env.svc = NewBudgetService(env.budgets, artifacts, platform, events, env.sender, nil, usageRepo)
	env.svc.(*budgetService).now = func() time.Time { return budgetTestNow }
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
		"consumers": [
			{"type": "attribute", "allocation": "shared", "expression": "'research' in request.Auth.Claims[\"groups\"]"},
			{"type": "attribute", "allocation": "separate", "expression": "request.Auth.Claims[\"email\"]"}
		],
		"exemptions": [{"type": "attribute", "expression": "request.Auth.Claims[\"role\"] == \"admin\""}],
		"warnings": {"thresholds": [80, 100], "channelIds": ["`+budgetOrgChannel+`"]}
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
	assert.Contains(t, string(out), `"currentPeriod":{"periodStart":"2026-11-01","spent":"0.00","unpricedRequests":0}`,
		"a new budget has spent nothing this month")
}

func TestBudgetService_CreateAllowsAMinimalBudget(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "", spec.BudgetRequest{
		Name: "org-monthly", OrgId: budgetTestOrgName, DisplayName: strPtr("Org monthly"),
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
		{"long display name", func(r *spec.BudgetRequest) { r.DisplayName = strPtr(strings.Repeat("x", 256)) }, utils.ErrCodeValidation, "displayName"},
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
		{"unknown project", func(r *spec.BudgetRequest) {
			r.Consumers = append(r.Consumers, spec.BudgetConsumer{Type: "project", Id: strPtr("other")})
		}, utils.ErrCodeReferenceNotFound, "consumers[2].id"},
		{"named agent without its project", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "agent", Id: strPtr("hotel-booking")}}
		}, utils.ErrCodeValidation, "consumers[0].id"},
		{"unknown agent", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("api-platform")}, {Type: "agent", Id: strPtr("nope")}}
		}, utils.ErrCodeReferenceNotFound, "consumers[1].id"},
		{"shared api key without its name", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "apiKey"}}
		}, utils.ErrCodeValidation, "consumers[0].id"},
		{"project used twice", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "project", Id: strPtr("api-platform")}, {Type: "project", Allocation: strPtr("separate")}}
		}, utils.ErrCodeValidation, "consumers[1].type"},
		{"unknown consumer type", func(r *spec.BudgetRequest) {
			r.Consumers = []spec.BudgetConsumer{{Type: "team"}}
		}, utils.ErrCodeValidation, "consumers[0].type"},
		{"attribute without expression", func(r *spec.BudgetRequest) {
			r.Consumers = append(r.Consumers, spec.BudgetConsumer{Type: "attribute"})
		}, utils.ErrCodeValidation, "consumers[2].expression"},
		{"attribute with id", func(r *spec.BudgetRequest) {
			r.Consumers[0].Id = strPtr("x")
		}, utils.ErrCodeValidation, "consumers[0].id"},
		{"unknown exempt agent", func(r *spec.BudgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "agent", Project: strPtr("api-platform"), Id: strPtr("nope")}}
		}, utils.ErrCodeReferenceNotFound, "exemptions[0].id"},
		{"attribute exemption without expression", func(r *spec.BudgetRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "attribute"}}
		}, utils.ErrCodeValidation, "exemptions[0]"},
		{"threshold out of range", func(r *spec.BudgetRequest) {
			r.Warnings.Thresholds = []int32{1001}
		}, utils.ErrCodeValidation, "warnings.thresholds[0]"},
		{"bad channel id", func(r *spec.BudgetRequest) {
			r.Warnings.ChannelIds = []string{"not-a-uuid"}
		}, utils.ErrCodeValidation, "warnings.channelIds[0]"},
		{"unknown provider", func(r *spec.BudgetRequest) { r.Resource.Id = strPtr("anthropic") }, utils.ErrCodeReferenceNotFound, "resource.id"},
		{"unknown channel", func(r *spec.BudgetRequest) {
			r.Warnings.ChannelIds = []string{uuid.NewString()}
		}, utils.ErrCodeChannelNotAvailable, "warnings.channelIds[0]"},
		{"project channel without that project", func(r *spec.BudgetRequest) {
			r.Warnings.ChannelIds = []string{budgetProjectChannel}
		}, utils.ErrCodeChannelNotAvailable, "warnings.channelIds[0]"},
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
			assert.Empty(t, env.sender.sent, "nothing is sent to gateways")
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
	cfg, project, err := gatewayBudgetConfig("research-monthly", json.RawMessage(`{
		"apiVersion": "gateway.api-platform.wso2.com/v1", "kind": "Budget",
		"metadata": {"name": "research-monthly"},
		"spec": {
			"limit": {"amount": "500.00", "period": "month", "action": "block", "blockAt": 95},
			"resource": {"type": "llmProvider", "id": "openai"},
			"consumers": [{"type": "attribute", "expression": "'research' in request.Auth.Claims[\"groups\"]"}]
		}
	}`))
	require.NoError(t, err)
	assert.Empty(t, project, "no project-id annotation: an organization budget")
	assert.Equal(t, "research-monthly", cfg.DisplayName, "a gateway budget shows its name")
	assert.Equal(t, "active", cfg.Status)
	assert.Equal(t, "USD", cfg.Limit.Currency)
	assert.Equal(t, int32(95), *cfg.Limit.BlockAt)
	assert.Equal(t, "shared", cfg.Resource.Allocation)
	assert.Equal(t, "shared", cfg.Consumers[0].Allocation)
	assert.NotNil(t, cfg.Exemptions)

	_, _, err = gatewayBudgetConfig("x", json.RawMessage(`not json`))
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

	page, err := env.svc.List(context.Background(), budgetTestOrg, "", nil, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, int32(3), page.Total)
	assert.Equal(t, int32(2), page.Limit)
	require.Len(t, page.Budgets, 2)
	assert.Equal(t, "a", page.Budgets[0].Name)

	page, err = env.svc.List(context.Background(), budgetTestOrg, "", nil, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int32(20), page.Limit, "default page size")
	require.Len(t, page.Budgets, 1)
	assert.Equal(t, "c", page.Budgets[0].Name)

	page, err = env.svc.List(context.Background(), budgetTestOrg, "", nil, 20, 10)
	require.NoError(t, err)
	assert.Empty(t, page.Budgets)
	assert.Equal(t, int32(3), page.Total)

	for filter, want := range map[string]int{
		"llmProvider:openai":   2,
		"project:api-platform": 0,
		"agent:hotel-booking":  0,
		"llmProvider:other":    0,
	} {
		page, err = env.svc.List(context.Background(), budgetTestOrg, "", []string{filter}, 20, 0)
		require.NoError(t, err)
		assert.Len(t, page.Budgets, want, filter)
	}
	page, err = env.svc.List(context.Background(), budgetTestOrg, "", []string{"project:api-platform", "llmProvider:other"}, 20, 0)
	require.NoError(t, err)
	assert.Empty(t, page.Budgets, "filters combine with AND")

	for _, bad := range [][]string{{"team:a"}, {"project"}, {"project:"}} {
		_, err = env.svc.List(context.Background(), budgetTestOrg, "", bad, 20, 0)
		assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err), bad)
	}
	_, err = env.svc.List(context.Background(), budgetTestOrg, "", nil, 101, 0)
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}

func TestBudgetService_UpdateReplacesTheEditableFields(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	inactive := "inactive"
	resp, err := env.svc.Update(context.Background(), budgetTestOrg, "team-a", spec.BudgetUpdateRequest{
		DisplayName: strPtr("Team A"),
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
		DisplayName: strPtr("Team A"), Limit: spec.BudgetUpdateLimit{Amount: "150.00", Action: "allow"},
	})
	require.NoError(t, err)
	assert.Equal(t, "active", resp.Status, "status defaults to active")
	assert.Nil(t, resp.Limit.BlockAt)
}

func TestBudgetService_UpdateRefusals(t *testing.T) {
	ok := func() spec.BudgetUpdateRequest {
		return spec.BudgetUpdateRequest{DisplayName: strPtr("Team A"), Limit: spec.BudgetUpdateLimit{Amount: "150.00", Action: "block"}}
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

func TestBudgetService_SendsBudgetsToGateways(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	inactive := "inactive"
	_, err := env.svc.Update(context.Background(), budgetTestOrg, "team-a", spec.BudgetUpdateRequest{
		DisplayName: strPtr("Team A"), Status: &inactive, Limit: spec.BudgetUpdateLimit{Amount: "150.00", Action: "block"},
	})
	require.NoError(t, err)
	require.NoError(t, env.svc.Delete(auditableCtx(t), budgetTestOrg, "team-a"))
	assert.Equal(t, []string{"created:team-a:active", "updated:team-a:inactive", "deleted:team-a"}, env.sender.sent,
		"disabling is an update")

	_, err = env.svc.Update(context.Background(), budgetTestOrg, "team-a", spec.BudgetUpdateRequest{
		DisplayName: strPtr("x"), Limit: spec.BudgetUpdateLimit{Amount: "1", Action: "allow"},
	})
	require.Error(t, err)
	assert.Len(t, env.sender.sent, 3, "a refused request sends nothing")
}

func TestBuildBudgetKind_ForGatewayNamesAResource(t *testing.T) {
	cfg := models.BudgetConfig{
		DisplayName: "Org", Status: "active",
		Limit:     models.BudgetLimit{Amount: "10", Currency: "USD", Period: "day", Action: "allow"},
		Consumers: []models.BudgetConsumer{}, Exemptions: []models.BudgetExemption{},
	}
	gw, err := json.Marshal(BuildBudgetKind("org", cfg, true))
	require.NoError(t, err)
	assert.Contains(t, string(gw), `"resource":{"type":"llmProvider","allocation":"shared"}`, "every provider, shared")
	assert.Nil(t, cfg.Resource, "the stored budget is not changed")
}

func TestBudgetService_GatewayBudgetsTakeOnlyAgentManagerFields(t *testing.T) {
	env := newBudgetTestEnv(t)
	gw := uuid.New()
	env.store["from-gateway"] = models.Budget{
		ID: uuid.New(), Name: "from-gateway", OrgID: budgetTestOrg, Origin: models.BudgetOriginGateway, GatewayID: &gw,
		Configuration: models.BudgetConfig{
			DisplayName: "from-gateway", Status: "active",
			// As a gateway pushes it: no blockAt means block at 100%.
			Limit:      models.BudgetLimit{Amount: "500.00", Currency: "USD", Period: "month", Action: "block"},
			Consumers:  []models.BudgetConsumer{{Type: "attribute", Allocation: "shared", Expression: strPtr(budgetGroupExpr)}},
			Exemptions: []models.BudgetExemption{},
		},
	}
	same := func() spec.BudgetUpdateRequest {
		return spec.BudgetUpdateRequest{
			DisplayName: strPtr("Research, monthly"), Description: strPtr("Set on the research gateway."),
			Limit:    spec.BudgetUpdateLimit{Amount: "500.00", Action: "block"},
			Warnings: &spec.BudgetWarnings{Thresholds: []int32{80, 100}, ChannelIds: []string{budgetOrgChannel}},
		}
	}

	resp, err := env.svc.Update(context.Background(), budgetTestOrg, "from-gateway", same())
	require.NoError(t, err)
	assert.Equal(t, "Research, monthly", resp.DisplayName)
	assert.Equal(t, "Set on the research gateway.", *resp.Description)
	require.NotNil(t, resp.Warnings)
	assert.Equal(t, "gateway_api", resp.Origin)
	stored := env.store["from-gateway"]
	assert.Equal(t, models.BudgetOriginGateway, stored.Origin)
	assert.Equal(t, "Research, monthly", stored.Configuration.DisplayName)
	assert.Empty(t, env.sender.sent, "fields gateways never get: nothing is sent")

	for name, edit := range map[string]func(*spec.BudgetUpdateRequest){
		"amount":  func(r *spec.BudgetUpdateRequest) { r.Limit.Amount = "600.00" },
		"blockAt": func(r *spec.BudgetUpdateRequest) { r.Limit.BlockAt = budgetI32(90) },
		"action":  func(r *spec.BudgetUpdateRequest) { r.Limit.Action = "allow" },
		"status":  func(r *spec.BudgetUpdateRequest) { r.Status = strPtr("inactive") },
		"exemptions": func(r *spec.BudgetUpdateRequest) {
			r.Exemptions = []spec.BudgetExemption{{Type: "attribute", Expression: strPtr(budgetAdminExpr)}}
		},
	} {
		req := same()
		edit(&req)
		_, err := env.svc.Update(context.Background(), budgetTestOrg, "from-gateway", req)
		assert.Equal(t, utils.ErrCodeBudgetReadOnly, budgetErrorCode(t, err), name)
	}
	assert.Equal(t, "500.00", env.store["from-gateway"].Configuration.Limit.Amount)

	err = env.svc.Delete(auditableCtx(t), budgetTestOrg, "from-gateway")
	assert.Equal(t, utils.ErrCodeBudgetReadOnly, budgetErrorCode(t, err), "only the gateway deletes its budget")
}

func TestBudgetService_ProjectAgentAndAPIKeyBudgets(t *testing.T) {
	env := newBudgetTestEnv(t)
	req := budgetTestRequest()
	req.Consumers = []spec.BudgetConsumer{
		{Type: "project", Id: strPtr("api-platform")},
		{Type: "agent", Id: strPtr("hotel-booking")},
		{Type: "apiKey", Allocation: strPtr("separate")},
	}
	req.Exemptions = []spec.BudgetExemption{{Type: "attribute", Expression: strPtr(budgetAdminExpr)}}
	resp, err := env.svc.Create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
	require.Len(t, resp.Consumers, 3)
	assert.Nil(t, env.created[0].Configuration.Consumers[1].Project, "the stored agent names no project: the project consumer does")

	kind := BuildBudgetKind(resp.Name, env.created[0].Configuration, true)
	agent := kind.Spec.Consumers[1]
	require.NotNil(t, agent.Project, "gateways get the agent with its project")
	assert.Equal(t, "api-platform", *agent.Project)
	assert.Equal(t, "hotel-booking", *agent.ID)

	page, err := env.svc.List(context.Background(), budgetTestOrg, "", []string{"agent:hotel-booking", "project:api-platform"}, 20, 0)
	require.NoError(t, err)
	assert.Len(t, page.Budgets, 1)
}

func spendRow(budgetID uuid.UUID, allowance map[string]string, start time.Time, spent string, unpriced int64) models.BudgetUsage {
	return models.BudgetUsage{
		BudgetID: budgetID, AllowanceID: uuid.New(), Allowance: allowance, PeriodStart: start, Spent: spent, UnpricedRequests: unpriced,
	}
}

func TestBudgetPeriodStart(t *testing.T) {
	assert.Equal(t, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), budgetPeriodStart("day", budgetTestNow))
	assert.Equal(t, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), budgetPeriodStart("week", budgetTestNow), "weeks start Monday")
	sunday := time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), budgetPeriodStart("week", sunday), "Sunday ends the week")
	assert.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), budgetPeriodStart("month", budgetTestNow))
	ist := time.FixedZone("IST", 5*3600+1800)
	assert.Equal(t, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
		budgetPeriodStart("day", time.Date(2026, 11, 1, 2, 0, 0, 0, ist)), "UTC calendar, whatever the caller's zone")
}

func TestBudgetService_CurrentPeriodSumsThisPeriodsSpend(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	createTestBudget(t, env, "team-b")
	a, b := env.store["team-a"].ID, env.store["team-b"].ID
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	env.usage = []models.BudgetUsage{
		spendRow(a, map[string]string{"user": "alice"}, november, "80.144", 2),
		spendRow(a, map[string]string{"user": "bob"}, november, "20.006", 1),
		spendRow(a, map[string]string{"user": "carol"}, october, "99", 7),
		spendRow(b, map[string]string{"user": "dave"}, november, "5", 0),
	}

	got, err := env.svc.Get(context.Background(), budgetTestOrg, "team-a")
	require.NoError(t, err)
	require.NotNil(t, got.CurrentPeriod)
	assert.Equal(t, spec.BudgetCurrentPeriod{PeriodStart: "2026-11-01", Spent: "100.15", UnpricedRequests: 3}, *got.CurrentPeriod,
		"October's row counts as $0")

	list, err := env.svc.List(context.Background(), budgetTestOrg, "", nil, 20, 0)
	require.NoError(t, err)
	spent := map[string]string{}
	for _, r := range list.Budgets {
		spent[r.Name] = r.CurrentPeriod.Spent
	}
	assert.Equal(t, map[string]string{"team-a": "100.15", "team-b": "5.00"}, spent)
}

func TestBudgetService_UsageListsAllowancesByHighestSpend(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "team-a")
	id := env.store["team-a"].ID
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	env.usage = []models.BudgetUsage{
		spendRow(id, map[string]string{"user": "bob"}, november, "20", 1),
		spendRow(id, map[string]string{"user": "alice"}, november, "80.14", 2),
		spendRow(id, map[string]string{"user": "carol"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), "99", 7),
		spendRow(id, map[string]string{"user": "erin"}, november, "20", 0),
	}

	page, err := env.svc.Usage(context.Background(), budgetTestOrg, "team-a", 3, 0)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-01", page.PeriodStart)
	assert.Equal(t, int32(4), page.Total)
	require.Len(t, page.Allowances, 3)
	assert.Equal(t, spec.BudgetAllowanceUsage{Allowance: map[string]string{"user": "alice"}, Spent: "80.14", UnpricedRequests: 2}, page.Allowances[0])
	assert.Equal(t, "bob", page.Allowances[1].Allowance["user"], "equal spend: ordered by allowance")
	assert.Equal(t, "erin", page.Allowances[2].Allowance["user"])

	last, err := env.svc.Usage(context.Background(), budgetTestOrg, "team-a", 3, 3)
	require.NoError(t, err)
	require.Len(t, last.Allowances, 1)
	assert.Equal(t, spec.BudgetAllowanceUsage{Allowance: map[string]string{"user": "carol"}, Spent: "0.00", UnpricedRequests: 0}, last.Allowances[0],
		"a row from an earlier period has spent nothing this period")

	_, err = env.svc.Usage(context.Background(), budgetTestOrg, "nope", 20, 0)
	assert.Equal(t, utils.ErrCodeBudgetNotFound, budgetErrorCode(t, err))
	_, err = env.svc.Usage(context.Background(), budgetTestOrg, "team-a", 101, 0)
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}
