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
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// costTestNow is in November 2026: the kept months are September to November.
var costTestNow = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func costRowFor(month time.Month, project, agent, apiKey, user, provider, model, cost string, missing int64) models.CostUsage {
	opt := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	return models.CostUsage{
		OUID: budgetTestOrg, Month: time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC), CostKey: uuid.New(),
		Project: opt(project), Agent: opt(agent), APIKey: opt(apiKey), UserID: opt(user),
		LLMProvider: provider, Model: opt(model), Cost: cost, MissingPriceRequests: missing,
	}
}

func newCostTestService(rows []models.CostUsage) (*costService, *repomocks.CostUsageRepositoryMock) {
	repo := &repomocks.CostUsageRepositoryMock{
		SinceFunc: func(_ context.Context, ouID string, from time.Time) ([]models.CostUsage, error) {
			out := []models.CostUsage{}
			for _, r := range rows {
				if r.OUID == ouID && !r.Month.Before(from) {
					out = append(out, r)
				}
			}
			return out, nil
		},
		RecordFunc:       func(context.Context, models.CostUsage) error { return nil },
		DeleteBeforeFunc: func(context.Context, time.Time) (int64, error) { return 0, nil },
	}
	s := NewCostService(repo).(*costService)
	s.now = func() time.Time { return costTestNow }
	return s, repo
}

var costTestRows = []models.CostUsage{
	costRowFor(time.November, "api-platform", "support-agent", "support-prod", "", "openai", "gpt-4o", "80.14", 3),
	costRowFor(time.October, "api-platform", "support-agent", "support-prod", "", "openai", "gpt-4o", "100", 0),
	costRowFor(time.November, "api-platform", "hotel-booking", "", "", "anthropic", "claude", "20.006", 0),
	costRowFor(time.November, "", "", "", "alice@wso2.com", "openai", "gpt-4o", "5", 1),
	costRowFor(time.May, "api-platform", "support-agent", "", "", "openai", "gpt-4o", "999", 0),
}

func TestCostService_OrganizationTotalPerMonth(t *testing.T) {
	s, _ := newCostTestService(costTestRows)
	report, err := s.Costs(context.Background(), budgetTestOrg, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "organization", report.GroupBy)
	assert.Equal(t, []string{"2026-09", "2026-10", "2026-11"}, report.Months)
	require.Len(t, report.Groups, 1)
	g := report.Groups[0]
	assert.Equal(t, []string{"0.00", "100.00", "105.15"}, g.Costs, "May is older than the kept months")
	assert.Equal(t, "205.15", g.Total)
	assert.Equal(t, int64(4), g.MissingPriceRequests)
}

func TestCostService_GroupsHighestTotalFirst(t *testing.T) {
	s, _ := newCostTestService(costTestRows)
	report, err := s.Costs(context.Background(), budgetTestOrg, "agent", nil)
	require.NoError(t, err)
	require.Len(t, report.Groups, 3)
	first := report.Groups[0]
	assert.Equal(t, "api-platform", *first.Project, "an agent names its project")
	assert.Equal(t, "support-agent", *first.Agent)
	assert.Equal(t, "180.14", first.Total)
	assert.Equal(t, "hotel-booking", *report.Groups[1].Agent)
	assert.Nil(t, report.Groups[2].Agent, "requests with no agent are a group of their own")
	assert.Equal(t, "5.00", report.Groups[2].Total)
}

func TestCostService_Filters(t *testing.T) {
	s, _ := newCostTestService(costTestRows)
	report, err := s.Costs(context.Background(), budgetTestOrg, "model",
		[]string{"agent:api-platform/support-agent,api-platform/hotel-booking", "llmProvider:openai"})
	require.NoError(t, err)
	require.Len(t, report.Groups, 1, "a comma list is any of them; a repeated filter is AND")
	assert.Equal(t, "gpt-4o", *report.Groups[0].Model)
	assert.Equal(t, "180.14", report.Groups[0].Total)

	report, err = s.Costs(context.Background(), budgetTestOrg, "user", []string{"user:alice@wso2.com"})
	require.NoError(t, err)
	require.Len(t, report.Groups, 1)
	assert.Equal(t, "alice@wso2.com", *report.Groups[0].User)
}

func TestCostService_RefusesBadQueries(t *testing.T) {
	s, _ := newCostTestService(nil)
	for _, tc := range []struct {
		groupBy string
		filters []string
		field   string
	}{
		{"environment", nil, "groupBy"},
		{"", []string{"environment:production"}, "filter"},
		{"", []string{"project"}, "filter"},
		{"", []string{"project:"}, "filter"},
	} {
		_, err := s.Costs(context.Background(), budgetTestOrg, tc.groupBy, tc.filters)
		var be *BudgetError
		require.ErrorAs(t, err, &be, "%v", tc)
		assert.Equal(t, utils.ErrCodeValidation, be.Code)
		assert.Equal(t, tc.field, be.Field)
	}
	report, err := s.Costs(context.Background(), budgetTestOrg, "project", nil)
	require.NoError(t, err)
	assert.Empty(t, report.Groups, "no costs yet")
	assert.NotNil(t, report.Groups)
	assert.Len(t, report.Months, 3)
}

func TestCostService_RecordsReportsAndSkipsBadOrOldOnes(t *testing.T) {
	s, repo := newCostTestService(nil)
	key := uuid.New().String()
	s.RecordCosts(context.Background(), budgetTestOrg, []GatewayCostUsage{
		{Month: "2026-11-01", CostKey: key, Project: "api-platform", Agent: "support-agent", LLMProvider: "openai", Model: "gpt-4o", Cost: "80.14", MissingPriceRequests: 3},
		{Month: "2026-08-01", CostKey: uuid.New().String(), LLMProvider: "openai", Cost: "1"},
		{Month: "not-a-date", CostKey: uuid.New().String(), LLMProvider: "openai", Cost: "1"},
		{Month: "2026-11-01", CostKey: "x", LLMProvider: "openai", Cost: "1"},
		{Month: "2026-11-01", CostKey: uuid.New().String(), Cost: "1"},
		{Month: "2026-11-01", CostKey: uuid.New().String(), LLMProvider: "openai", Cost: "ten"},
	})
	calls := repo.RecordCalls()
	require.Len(t, calls, 1, "older than the kept months, or unreadable: skipped")
	row := calls[0].U
	assert.Equal(t, key, row.CostKey.String())
	assert.Equal(t, "api-platform", *row.Project)
	assert.Nil(t, row.APIKey, "empty values are stored as none")
	assert.Equal(t, int64(3), row.MissingPriceRequests)
}

func TestCostService_DeletesMonthsOlderThanTheKeptOnes(t *testing.T) {
	s, repo := newCostTestService(nil)
	s.deleteOld(context.Background())
	require.Len(t, repo.DeleteBeforeCalls(), 1)
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), repo.DeleteBeforeCalls()[0].Before)
}
