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

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

var (
	budgetGwEgress  = uuid.MustParse("0193f1c2-0000-7000-8000-0000000000f1")
	budgetGwBoth    = uuid.MustParse("0193f1c2-0000-7000-8000-0000000000f2")
	budgetGwIngress = uuid.MustParse("0193f1c2-0000-7000-8000-0000000000f3")
)

// newBudgetGatewaysEnv has an egress and a both gateway, an ingress gateway, a
// control-plane budget and a gateway budget.
func newBudgetGatewaysEnv(t *testing.T) (*BudgetGateways, *recordingEventHub, *[]repositories.GatewayFilterOptions) {
	t.Helper()
	var filters []repositories.GatewayFilterOptions
	gateways := map[uuid.UUID]*models.Gateway{
		budgetGwEgress:  {UUID: budgetGwEgress, OUID: budgetTestOrg, GatewayFunctionalityType: models.GatewayRoleEgress},
		budgetGwBoth:    {UUID: budgetGwBoth, OUID: budgetTestOrg, GatewayFunctionalityType: models.GatewayRoleBoth},
		budgetGwIngress: {UUID: budgetGwIngress, OUID: budgetTestOrg, GatewayFunctionalityType: models.GatewayRoleIngress},
	}
	gatewayRepo := &repomocks.GatewayRepositoryMock{
		ListWithFiltersCtxFunc: func(_ context.Context, f repositories.GatewayFilterOptions) ([]*models.Gateway, error) {
			filters = append(filters, f)
			out := []*models.Gateway{}
			for _, gw := range []uuid.UUID{budgetGwEgress, budgetGwBoth} {
				out = append(out, gateways[gw])
			}
			return out, nil
		},
		GetByUUIDFunc: func(id string) (*models.Gateway, error) {
			return gateways[uuid.MustParse(id)], nil
		},
	}
	updated := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	budgets := &repomocks.BudgetRepositoryMock{
		ListFunc: func(context.Context, string) ([]models.Budget, error) {
			gw := budgetGwEgress
			return []models.Budget{
				{
					ID: uuid.MustParse("0193f1c2-0000-7000-8000-0000000000b1"), Name: "org-monthly", OrgID: budgetTestOrg,
					Origin: models.BudgetOriginControlPlane, Configuration: budgetConfigForTest("Org monthly"), UpdatedAt: updated,
				},
				{
					ID: uuid.New(), Name: "from-gateway", OrgID: budgetTestOrg, Origin: models.BudgetOriginGateway, GatewayID: &gw,
					Configuration: budgetConfigForTest("from-gateway"), UpdatedAt: updated,
				},
			}, nil
		},
	}
	hub := &recordingEventHub{}
	return NewBudgetGateways(NewGatewayEventsService(hub), gatewayRepo, budgets), hub, &filters
}

func budgetConfigForTest(displayName string) models.BudgetConfig {
	return models.BudgetConfig{
		DisplayName: displayName, Status: "active",
		Limit:      models.BudgetLimit{Amount: "5000", Currency: "USD", Period: "month", Action: "block", BlockAt: budgetI32(100)},
		Consumers:  []models.BudgetConsumer{{Type: "attribute", Allocation: "shared", Expression: strPtr(budgetGroupExpr)}},
		Exemptions: []models.BudgetExemption{},
		Warnings:   &models.BudgetWarnings{Thresholds: []int32{80}, Channels: []string{"finance-email"}},
	}
}

func TestBudgetGateways_SendsToTheEgressGatewaysOfTheBudget(t *testing.T) {
	g, hub, filters := newBudgetGatewaysEnv(t)
	b := &models.Budget{
		ID: uuid.MustParse("0193f1c2-0000-7000-8000-0000000000b1"), Name: "org-monthly", OrgID: budgetTestOrg,
		Origin: models.BudgetOriginControlPlane, Configuration: budgetConfigForTest("Org monthly"),
		UpdatedAt: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
	}
	g.SendBudget(context.Background(), b, true)

	require.Len(t, *filters, 1)
	assert.Equal(t, models.EgressGatewayRoles, (*filters)[0].FunctionalityTypeIn)
	assert.Nil(t, (*filters)[0].EnvironmentID, "every egress gateway of the organization")
	require.Len(t, hub.published, 2)
	assert.ElementsMatch(t, []string{budgetGwEgress.String(), budgetGwBoth.String()},
		[]string{hub.published[0].GatewayID, hub.published[1].GatewayID})

	evt := hub.published[0]
	assert.Equal(t, b.ID.String(), evt.EntityID, "keyed by the budget, so a replay keeps its latest state")
	var envelope struct {
		Type    string               `json:"type"`
		Payload BudgetGatewayPayload `json:"payload"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.EventData), &envelope))
	assert.Equal(t, BudgetEventCreated, envelope.Type)
	assert.Equal(t, b.ID.String(), envelope.Payload.BudgetID)
	assert.Equal(t, b.UpdatedAt, envelope.Payload.UpdatedAt)
	assert.Equal(t, "org-monthly", envelope.Payload.Budget.Metadata.Name)
	assert.Empty(t, envelope.Payload.Budget.Spec.DisplayName, "gateways never get displayName")
	assert.Nil(t, envelope.Payload.Budget.Spec.Warnings, "gateways never get warnings")
	assert.Equal(t, "llmProvider", envelope.Payload.Budget.Spec.Resource.Type)

	hub.published = nil
	g.SendBudget(context.Background(), b, false)
	g.SendBudgetDeleted(context.Background(), b)
	require.Len(t, hub.published, 4)
	require.NoError(t, json.Unmarshal([]byte(hub.published[0].EventData), &envelope))
	assert.Equal(t, BudgetEventUpdated, envelope.Type)
	require.NoError(t, json.Unmarshal([]byte(hub.published[2].EventData), &envelope))
	assert.Equal(t, BudgetEventDeleted, envelope.Type)
	assert.JSONEq(t, `{"budgetId":"`+b.ID.String()+`"}`, envelopePayload(t, hub.published[2].EventData))
}

func envelopePayload(t *testing.T, data string) string {
	t.Helper()
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &envelope))
	return string(envelope.Payload)
}

func TestBudgetGateways_NeverSendsAGatewaysOwnBudget(t *testing.T) {
	g, hub, _ := newBudgetGatewaysEnv(t)
	gw := budgetGwEgress
	g.SendBudget(context.Background(), &models.Budget{
		ID: uuid.New(), Name: "x", OrgID: budgetTestOrg,
		Origin: models.BudgetOriginGateway, GatewayID: &gw, Configuration: budgetConfigForTest("x"),
	}, true)
	assert.Empty(t, hub.published)
}

func TestBudgetGateways_ForGatewayListsTheControlPlaneBudgetsItEnforces(t *testing.T) {
	g, _, _ := newBudgetGatewaysEnv(t)
	got, err := g.ForGatewayID(context.Background(), budgetTestOrg, budgetGwEgress.String())
	require.NoError(t, err)
	require.Len(t, got, 1, "the gateway's own budget is left out")
	assert.Equal(t, "0193f1c2-0000-7000-8000-0000000000b1", got[0].BudgetID)
	assert.Equal(t, "org-monthly", got[0].Budget.Metadata.Name)

	got, err = g.ForGatewayID(context.Background(), budgetTestOrg, budgetGwIngress.String())
	require.NoError(t, err)
	assert.Empty(t, got, "an ingress gateway enforces no budgets")
	assert.NotNil(t, got, "an empty list, not null")

	_, err = g.ForGatewayID(context.Background(), "another-org", budgetGwEgress.String())
	assert.ErrorIs(t, err, utils.ErrGatewayNotFound)
}
