//go:build integration

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

package repositories

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/db"
	"github.com/wso2/agent-manager/agent-manager-service/db/dbtest"
	"github.com/wso2/agent-manager/agent-manager-service/models"
)

func TestBudgetUsageRepository_RecordMergesAndQueuesWarningsOnce(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou := dbtest.Org(t, gdb)
	budgetID := createArtifact(t, gdb, ou, models.KindBudget, "per-agent")
	events := NewEventRepository(gdb)
	dest := &models.EventDestination{
		ID: uuid.New(), OUID: ou, Name: "finance", Kind: models.DestinationKindWebhook,
		Config: json.RawMessage(`{"url":"https://example.com"}`), Availability: models.DestinationAvailableAll, Projects: []string{},
	}
	require.NoError(t, events.CreateDestination(ctx, dest))
	repo := NewBudgetUsageRepository(gdb)

	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	allowance := uuid.New()
	report := func(period time.Time, spent string, unpriced int64) models.BudgetUsage {
		return models.BudgetUsage{
			BudgetID: budgetID, AllowanceID: allowance, OUID: ou, Allowance: map[string]string{"agent": "support-agent"},
			PeriodStart: period, Spent: spent, UnpricedRequests: unpriced,
		}
	}
	var seen []models.BudgetUsage
	warn := func(stored models.BudgetUsage) []models.EventDelivery {
		seen = append(seen, stored)
		return []models.EventDelivery{{
			BudgetID: &budgetID, OUID: ou, DestinationID: dest.ID, SubscriptionIDs: []string{},
			EventID: "budget:" + budgetID.String() + ":80", EventType: "budget.threshold_crossed",
		}}
	}

	queued, err := repo.Record(ctx, report(november, "80.14", 3), warn)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the first crossing is queued")
	assert.Equal(t, models.EventDeliveryPending, queued[0].Status)
	got, err := events.GetDelivery(ctx, dest.ID, queued[0].EventID)
	require.NoError(t, err)
	require.NotNil(t, got.BudgetID)
	assert.Equal(t, budgetID, *got.BudgetID)

	queued, err = repo.Record(ctx, report(november, "90", 2), warn)
	require.NoError(t, err)
	assert.Empty(t, queued, "the same warning is queued once")
	rows, err := repo.ForBudget(ctx, budgetID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "90", rows[0].Spent, "the higher total is kept")
	assert.Equal(t, int64(3), rows[0].UnpricedRequests, "and the higher unpriced count")
	assert.Equal(t, map[string]string{"agent": "support-agent"}, rows[0].Allowance)

	seen = nil
	_, err = repo.Record(ctx, report(november, "85", 3), warn)
	require.NoError(t, err)
	assert.Empty(t, seen, "a lower total changes nothing, so nothing is checked")

	_, err = repo.Record(ctx, report(november.AddDate(0, -1, 0), "500", 9), warn)
	require.NoError(t, err)
	rows, _ = repo.ForBudget(ctx, budgetID)
	assert.Equal(t, "90", rows[0].Spent, "an earlier period is ignored")

	december := november.AddDate(0, 1, 0)
	_, err = repo.Record(ctx, report(december, "1.5", 0), nil)
	require.NoError(t, err)
	rows, _ = repo.ForBudget(ctx, budgetID)
	assert.Equal(t, "1.5", rows[0].Spent, "a new period replaces the row")
	assert.True(t, rows[0].PeriodStart.Equal(december))

	more, err := repo.QueueWarnings(ctx, []models.EventDelivery{{
		BudgetID: &budgetID, OUID: ou, DestinationID: dest.ID, SubscriptionIDs: []string{},
		EventID: "budget:" + budgetID.String() + ":dec", EventType: "budget.threshold_crossed",
	}})
	require.NoError(t, err)
	assert.Len(t, more, 1)
}

// The budgets list reads the usage of a page of budgets in one query, within the organization.
func TestBudgetUsageRepository_ForBudgets(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou, other := dbtest.Org(t, gdb), dbtest.Org(t, gdb)
	a := createArtifact(t, gdb, ou, models.KindBudget, "team-a")
	b := createArtifact(t, gdb, ou, models.KindBudget, "team-b")
	elsewhere := createArtifact(t, gdb, other, models.KindBudget, "team-a")
	repo := NewBudgetUsageRepository(gdb)
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		budget uuid.UUID
		org    string
	}{{a, ou}, {a, ou}, {b, ou}, {elsewhere, other}} {
		_, err := repo.Record(ctx, models.BudgetUsage{
			BudgetID: row.budget, AllowanceID: uuid.New(), OUID: row.org, Allowance: map[string]string{},
			PeriodStart: november, Spent: "1", UpdatedAt: november,
		}, nil)
		require.NoError(t, err)
	}
	rows, err := repo.ForBudgets(ctx, ou, []uuid.UUID{a, b, elsewhere})
	require.NoError(t, err)
	assert.Len(t, rows, 3, "another organization's usage is never read")
	none, err := repo.ForBudgets(ctx, ou, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
