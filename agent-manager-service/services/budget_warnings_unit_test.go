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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/events"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/services/eventdestinations"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// warningsEnv is a BudgetWarningService over mocks: one budget "per-agent" in
// project api-platform ($100 a month, warnings at 80 and 100), three channels (one
// available, one org-wide, one for another project) and a usage store that keeps rows.
type warningsEnv struct {
	svc      *BudgetWarningService
	budget   *models.Budget
	channels []models.EventDestination
	rows     map[uuid.UUID]models.BudgetUsage
	queued   []models.EventDelivery
	bus      *queueBus
}

func newWarningsEnv(t *testing.T) *warningsEnv {
	t.Helper()
	project := "api-platform"
	env := &warningsEnv{rows: map[uuid.UUID]models.BudgetUsage{}, bus: &queueBus{enqueued: map[string][]byte{}}}
	env.channels = []models.EventDestination{
		{ID: uuid.New(), OUID: budgetTestOrg, Name: "all-email", Availability: models.DestinationAvailableAll},
		{ID: uuid.New(), OUID: budgetTestOrg, Name: "org-slack", Availability: models.DestinationAvailableOrg},
		{ID: uuid.New(), OUID: budgetTestOrg, Name: "other-webhook", Availability: models.DestinationAvailableProjects, Projects: []string{"other"}},
	}
	names := []string{}
	for _, c := range env.channels {
		names = append(names, c.Name)
	}
	env.budget = &models.Budget{
		ID: uuid.MustParse("0193f1c2-0000-7000-8000-0000000000b1"), Name: "per-agent", OrgID: budgetTestOrg,
		Origin: models.BudgetOriginControlPlane,
		Configuration: models.BudgetConfig{
			DisplayName: "API Platform, per agent", Status: budgetStatusActive,
			Limit: models.BudgetLimit{Amount: "100.00", Currency: "USD", Period: "month", Action: "block"},
			Consumers: []models.BudgetConsumer{
				{Type: budgetTypeProject, Allocation: "shared", ID: &project},
				{Type: budgetTypeAgent, Allocation: "separate"},
			},
			Warnings: &models.BudgetWarnings{Thresholds: []int32{80, 100}, Channels: names},
		},
	}
	budgets := &repomocks.BudgetRepositoryMock{
		GetFunc: func(_ context.Context, ouID, name string) (*models.Budget, error) {
			if ouID == budgetTestOrg && name == env.budget.Name {
				return env.budget, nil
			}
			return nil, utils.ErrBudgetNotFound
		},
	}
	usage := &repomocks.BudgetUsageRepositoryMock{
		RecordFunc: func(_ context.Context, u models.BudgetUsage, warn func(models.BudgetUsage) []models.EventDelivery) ([]models.EventDelivery, error) {
			env.rows[u.AllowanceID] = u
			if warn == nil {
				return nil, nil
			}
			deliveries := warn(u)
			env.queued = append(env.queued, deliveries...)
			return deliveries, nil
		},
		ForBudgetFunc: func(context.Context, uuid.UUID) ([]models.BudgetUsage, error) {
			out := []models.BudgetUsage{}
			for _, r := range env.rows {
				out = append(out, r)
			}
			return out, nil
		},
		QueueWarningsFunc: func(_ context.Context, d []models.EventDelivery) ([]models.EventDelivery, error) {
			env.queued = append(env.queued, d...)
			return d, nil
		},
	}
	eventRepo := &repomocks.EventRepositoryMock{
		ListDestinationsFunc: func(_ context.Context, ouID string) ([]models.EventDestination, error) {
			assert.Equal(t, budgetTestOrg, ouID)
			// The organization's other channels are not the budget's.
			return append([]models.EventDestination{{ID: uuid.New(), OUID: ouID, Name: "unused", Availability: models.DestinationAvailableAll}},
				env.channels...), nil
		},
	}
	gateways := &repomocks.GatewayRepositoryMock{
		GetByUUIDFunc: func(string) (*models.Gateway, error) { return &models.Gateway{OUID: budgetTestOrg}, nil },
	}
	env.svc = NewBudgetWarningService(budgets, usage, eventRepo, gateways, env.bus, nil)
	return env
}

func usagePayload(t *testing.T, allowances ...GatewayAllowanceUsage) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(GatewayUsageMessage{Allowances: allowances})
	require.NoError(t, err)
	return data
}

func supportAgentUsage(spent string) GatewayAllowanceUsage {
	return GatewayAllowanceUsage{
		Budget: "per-agent", PeriodStart: "2026-11-01", AllowanceUUID: "5d6e2c1a-8f3b-5c47-a1d9-7e2b4c6f0a83",
		Allowance: map[string]string{"agent": "api-platform/support-agent"}, Spent: spent, UnpricedRequests: 3,
	}
}

func TestBudgetWarnings_ACrossedThresholdWarnsEachAvailableChannelOnce(t *testing.T) {
	env := newWarningsEnv(t)
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, supportAgentUsage("80.14")))

	row := env.rows[uuid.MustParse("5d6e2c1a-8f3b-5c47-a1d9-7e2b4c6f0a83")]
	assert.Equal(t, map[string]string{"project": "api-platform", "agent": "support-agent"}, row.Allowance,
		"an agent is stored as project and agent")
	assert.Equal(t, "80.14", row.Spent)
	assert.True(t, row.PeriodStart.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))

	require.Len(t, env.queued, 2, "80% crossed, 100% not; the other project's channel is left out")
	assert.ElementsMatch(t, []uuid.UUID{env.channels[0].ID, env.channels[1].ID},
		[]uuid.UUID{env.queued[0].DestinationID, env.queued[1].DestinationID})
	d := env.queued[0]
	wantID := "budget:0193f1c2-0000-7000-8000-0000000000b1:2026-11-01:5d6e2c1a-8f3b-5c47-a1d9-7e2b4c6f0a83:80.00"
	assert.Equal(t, wantID, d.EventID)
	assert.Equal(t, env.budget.ID, *d.BudgetID)
	assert.Equal(t, "agent", d.EventScope)
	assert.Equal(t, "api-platform", d.ProjectName)
	assert.Equal(t, "support-agent", d.AgentName)

	require.Len(t, env.bus.enqueued, 2)
	var job eventDeliveryJob
	require.NoError(t, json.Unmarshal(env.bus.enqueued[wantID+":"+env.channels[0].ID.String()], &job))
	var evt struct {
		events.Event
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(job.Event, &evt))
	assert.Equal(t, "com.wso2.agentmanager.budget.threshold_crossed", evt.Type)
	assert.Equal(t, "/agent-manager/orgs/"+budgetTestOrg+"/projects/api-platform/agents/support-agent", evt.Source)
	assert.Equal(t, "per-agent", evt.Data["budget"])
	assert.Equal(t, "API Platform, per agent", evt.Data["displayName"])
	assert.Equal(t, "80.14", evt.Data["spent"])
	assert.EqualValues(t, 80, evt.Data["threshold"])
	assert.Equal(t, "2026-12-01T00:00:00Z", evt.Data["resetsAt"])
	assert.Equal(t, "support-agent at 80% ($80.14 of $100.00), resets 1 Dec", evt.Data["message"])
}

func TestBudgetWarnings_SkipsUnknownBudgetsAndInvalidTotals(t *testing.T) {
	env := newWarningsEnv(t)
	bad := supportAgentUsage("-1")
	unknown := supportAgentUsage("500")
	unknown.Budget = "gone"
	noUUID := supportAgentUsage("500")
	noUUID.AllowanceUUID = "x"
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, bad, unknown, noUUID))
	assert.Empty(t, env.rows)
	assert.Empty(t, env.bus.enqueued)
}

func TestBudgetWarnings_NoWarningsWithoutThresholdsOrWhenDisabled(t *testing.T) {
	env := newWarningsEnv(t)
	env.budget.Configuration.Status = budgetStatusInactive
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, supportAgentUsage("99")))
	assert.Len(t, env.rows, 1, "spend is still recorded")
	assert.Empty(t, env.queued)
}

func TestBudgetWarnings_AnEditCrossingStoredSpendWarns(t *testing.T) {
	env := newWarningsEnv(t)
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, supportAgentUsage("60")))
	assert.Empty(t, env.queued)

	env.budget.Configuration.Limit.Amount = "70.00"
	env.svc.CheckBudget(context.Background(), env.budget)
	require.Len(t, env.queued, 2, "60 is past 80% of the lowered $70 limit")
	assert.Contains(t, env.queued[0].EventID, ":56.00", "the warning names the new threshold amount, so it re-arms")
}

func TestBudgetWarnings_LabelsAndPeriods(t *testing.T) {
	assert.Equal(t, "Spend", allowanceLabel(nil))
	assert.Equal(t, "alice@wso2.com, openai", allowanceLabel(map[string]string{"attribute": "alice@wso2.com", "llmProvider": "openai"}))
	start := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, start.AddDate(0, 0, 1), periodEnd("day", start))
	assert.Equal(t, start.AddDate(0, 0, 7), periodEnd("week", start))
	assert.Equal(t, time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC), periodEnd("month", start))
}

// recordingWarner records the budgets an edit checks.
type recordingWarner struct{ checked []string }

func (w *recordingWarner) CheckBudget(_ context.Context, b *models.Budget) {
	w.checked = append(w.checked, b.Name)
}

func TestBudgetService_UpdateChecksWarnings(t *testing.T) {
	env := newBudgetTestEnv(t)
	resp, err := env.create(context.Background(), budgetTestOrg, "alice", budgetTestRequest())
	require.NoError(t, err)
	warner := &recordingWarner{}
	env.svc.(*budgetService).warner = warner

	_, err = env.update(context.Background(), budgetTestOrg, resp.Metadata.Name, budgetUpdateRequest{
		DisplayName: resp.Spec.DisplayName, Status: strPtr("active"),
		Limit: budgetUpdateLimit{Amount: "50.00", Action: "block"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{resp.Metadata.Name}, warner.checked)
}

// usageRecorder records the usage messages the gateway message handler passes on.
type usageRecorder struct {
	gateway string
	payload json.RawMessage
}

func (u *usageRecorder) HandleUsage(_ context.Context, gatewayID string, payload json.RawMessage) {
	u.gateway, u.payload = gatewayID, payload
}

func TestGatewayMessages_UsageGoesToTheWarningService(t *testing.T) {
	usage := &usageRecorder{}
	h := NewDeploymentAckHandler(nil, nil, usage)
	assert.True(t, h.HandleMessage("gw-1", []byte(`{"type":"usage","payload":{"allowances":[]}}`)))
	assert.Equal(t, "gw-1", usage.gateway)
	assert.JSONEq(t, `{"allowances":[]}`, string(usage.payload))
	assert.False(t, NewDeploymentAckHandler(nil, nil, nil).HandleMessage("gw-1", []byte(`{"type":"usage","payload":{}}`)),
		"without budgets the message is not handled")
}

func TestBudgetWarnings_RenderForChatAndEmail(t *testing.T) {
	env := newWarningsEnv(t)
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, supportAgentUsage("80.14")))
	var job eventDeliveryJob
	for _, body := range env.bus.enqueued {
		require.NoError(t, json.Unmarshal(body, &job))
		break
	}
	text := eventdestinations.Render(eventdestinations.Message{Type: events.TypePrefix + BudgetWarningEvent, Body: job.Event})
	assert.Contains(t, text.Title, "API Platform, per agent")
	assert.Contains(t, text.Body, "support-agent at 80% ($80.14 of $100.00), resets 1 Dec")
	t.Logf("title: %s\nbody: %s", text.Title, text.Body)
}

func TestBudgetWarnings_EventsOffRecordsSpendWithoutWarnings(t *testing.T) {
	env := newWarningsEnv(t)
	env.svc.bus = nil
	env.svc.HandleUsage(context.Background(), "gw-1", usagePayload(t, supportAgentUsage("99")))
	assert.Len(t, env.rows, 1, "spend is recorded")
	assert.Empty(t, env.queued, "nothing could send a warning")
	env.svc.CheckBudget(context.Background(), env.budget)
	assert.Empty(t, env.queued)
}

// recordedCosts records the cost totals passed on.
type recordedCosts struct {
	ouID  string
	costs []GatewayCostUsage
}

func (r *recordedCosts) RecordCosts(_ context.Context, ouID string, costs []GatewayCostUsage) {
	r.ouID, r.costs = ouID, append(r.costs, costs...)
}

// A usage message's costs (AI cost stats) are recorded for the gateway's organization,
// whether or not it carries allowances, and even when its budget is unknown.
func TestBudgetWarnings_UsageCostsAreRecorded(t *testing.T) {
	env := newWarningsEnv(t)
	recorder := &recordedCosts{}
	env.svc.costs = recorder
	cost := GatewayCostUsage{
		Month: "2026-11-01", CostKey: uuid.New().String(), Project: "api-platform", Agent: "support-agent",
		LLMProvider: "openai", Model: "gpt-4o", Cost: "80.14",
	}
	unknown := supportAgentUsage("1")
	unknown.Budget = "gone"
	data, err := json.Marshal(GatewayUsageMessage{Allowances: []GatewayAllowanceUsage{unknown}, Costs: []GatewayCostUsage{cost}})
	require.NoError(t, err)

	env.svc.HandleUsage(context.Background(), "gw-1", data)

	assert.Equal(t, budgetTestOrg, recorder.ouID)
	assert.Equal(t, []GatewayCostUsage{cost}, recorder.costs)
}
