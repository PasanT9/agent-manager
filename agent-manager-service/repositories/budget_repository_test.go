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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db"
	"github.com/wso2/agent-manager/agent-manager-service/db/dbtest"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// gatewayBudget builds a budget a gateway pushes.
func gatewayBudget(ouID string, gatewayID uuid.UUID, name, amount string) models.Budget {
	return models.Budget{
		OrgID: ouID, Origin: models.BudgetOriginGateway, GatewayID: &gatewayID,
		Configuration: budgetConfig(name, amount),
	}
}

func budgetConfig(displayName, amount string) models.BudgetConfig {
	return models.BudgetConfig{
		DisplayName: displayName, Status: "active",
		Limit:     models.BudgetLimit{Amount: amount, Currency: "USD", Period: "month", Action: "allow"},
		Consumers: []models.BudgetConsumer{}, Exemptions: []models.BudgetExemption{},
	}
}

// createArtifact stores an artifact of kind with the given handle, and for a budget its
// control-plane budgets row.
func createArtifact(t *testing.T, gdb *gorm.DB, ouID, kind, handle string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	require.NoError(t, gdb.Create(&models.Artifact{
		UUID: id, Handle: handle, Name: handle, Version: models.BudgetVersion, Kind: kind, OUID: ouID,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	if kind == models.KindBudget {
		require.NoError(t, gdb.Create(&models.Budget{
			ID: id, OrgID: ouID, Origin: models.BudgetOriginControlPlane,
			Configuration: budgetConfig(handle, "1000"),
		}).Error)
	}
	return id
}

func budgetsByName(t *testing.T, repo BudgetRepository, ouID string) map[string]models.Budget {
	t.Helper()
	rows, err := repo.List(context.Background(), ouID)
	require.NoError(t, err)
	out := map[string]models.Budget{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func keys(m map[string]models.Budget) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBudgetRepository_GatewayBudgets(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou := dbtest.Org(t, gdb)
	gw := dbtest.Gateway(t, gdb, ou, "egress").UUID
	other := dbtest.Gateway(t, gdb, ou, "egress").UUID
	repo := NewBudgetRepository(gdb)
	save := func(b models.Budget, name string) (models.Budget, bool) {
		t.Helper()
		stored, err := repo.SaveForGateway(ctx, &b, name)
		require.NoError(t, err)
		return b, stored
	}

	// A control-plane budget, another kind's artifact and another gateway's budget.
	createArtifact(t, gdb, ou, models.KindBudget, "org-wide")
	createArtifact(t, gdb, ou, models.KindLLMProvider, "openai")
	_, stored := save(gatewayBudget(ou, other, "team-z", "5"), "team-z")
	require.True(t, stored)

	a, stored := save(gatewayBudget(ou, gw, "team-a", "100"), "team-a")
	require.True(t, stored)
	_, stored = save(gatewayBudget(ou, gw, "team-b", "200"), "team-b")
	require.True(t, stored)
	for _, taken := range []string{"org-wide", "openai", "team-z"} {
		_, stored = save(gatewayBudget(ou, gw, taken, "1"), taken)
		assert.False(t, stored, "%s is another artifact's handle", taken)
	}
	all := budgetsByName(t, repo, ou)
	assert.ElementsMatch(t, []string{"org-wide", "team-z", "team-a", "team-b"}, keys(all))
	assert.Equal(t, models.BudgetOriginControlPlane, all["org-wide"].Origin)
	assert.Equal(t, "1000", all["org-wide"].Configuration.Limit.Amount)
	assert.Equal(t, other, *all["team-z"].GatewayID)
	var art models.Artifact
	require.NoError(t, gdb.Where("id = ?", a.ID).Take(&art).Error)
	assert.Equal(t, models.KindBudget, art.Kind)
	assert.Equal(t, "team-a", art.Handle)
	assert.False(t, art.InCatalog)

	// Agent Manager's fields of a gateway budget survive the gateway's next push.
	named, err := repo.Get(ctx, ou, "team-a")
	require.NoError(t, err)
	named.Configuration.DisplayName = "Team A, monthly"
	named.Configuration.Warnings = &models.BudgetWarnings{Thresholds: []int32{80}, ChannelIDs: []string{"3f2b8c1e-5a7d-4e9b-8c21-6d0f4a9e7b13"}}
	require.NoError(t, repo.Update(ctx, named))

	// A later push of team-a updates the stored budget and keeps its ID.
	again, stored := save(gatewayBudget(ou, gw, "team-a", "150"), "team-a")
	require.True(t, stored)
	assert.Equal(t, a.ID, again.ID, "the control plane's ID is kept across pushes")
	pushed := budgetsByName(t, repo, ou)["team-a"]
	assert.Equal(t, "150", pushed.Configuration.Limit.Amount)
	assert.Equal(t, "Team A, monthly", pushed.Configuration.DisplayName, "Agent Manager's display name is kept")
	require.NotNil(t, pushed.Configuration.Warnings, "Agent Manager's warnings are kept")

	// A deletion removes only that gateway's budget of the name, artifact and row.
	require.NoError(t, repo.DeleteForGateway(ctx, ou, other, []string{"team-a"}))
	require.NoError(t, repo.DeleteForGateway(ctx, ou, gw, []string{"team-b", "unknown", "org-wide"}))
	all = budgetsByName(t, repo, ou)
	assert.ElementsMatch(t, []string{"org-wide", "team-z", "team-a"}, keys(all))
	var count int64
	require.NoError(t, gdb.Model(&models.Artifact{}).Where("org_id = ? AND handle = ?", ou, "team-b").Count(&count).Error)
	assert.Zero(t, count, "the artifact goes with the budget")

	// A complete list removes the gateway's budgets it does not name, and nothing else.
	_, _ = save(gatewayBudget(ou, gw, "team-c", "300"), "team-c")
	require.NoError(t, repo.DeleteForGatewayExcept(ctx, ou, gw, []string{"team-c"}))
	assert.ElementsMatch(t, []string{"org-wide", "team-z", "team-c"}, keys(budgetsByName(t, repo, ou)))

	// Deleting a gateway deletes the budgets it pushed.
	require.NoError(t, NewGatewayRepo(gdb).Delete(gw.String(), ou))
	assert.ElementsMatch(t, []string{"org-wide", "team-z"}, keys(budgetsByName(t, repo, ou)))
}

func TestBudgetRepository_CreateForControlPlane(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou := dbtest.Org(t, gdb)
	repo := NewBudgetRepository(gdb)
	createArtifact(t, gdb, ou, models.KindLLMProvider, "openai")

	b := models.Budget{
		OrgID: ou, Origin: models.BudgetOriginControlPlane, CreatedBy: "alice",
		Configuration: budgetConfig("Org monthly", "5000"),
	}
	require.NoError(t, repo.CreateForControlPlane(ctx, &b, "org-monthly"))
	assert.NotEqual(t, uuid.Nil, b.ID)
	assert.Equal(t, "org-monthly", b.Name)
	assert.False(t, b.CreatedAt.IsZero())

	stored := budgetsByName(t, repo, ou)["org-monthly"]
	assert.Equal(t, b.ID, stored.ID)
	assert.Equal(t, "alice", stored.CreatedBy)
	assert.Nil(t, stored.GatewayID)
	assert.False(t, stored.CreatedAt.IsZero(), "timestamps come from the artifact")
	assert.Equal(t, b.Configuration, stored.Configuration)

	for _, taken := range []string{"org-monthly", "openai"} {
		again := models.Budget{OrgID: ou, Origin: models.BudgetOriginControlPlane, Configuration: budgetConfig(taken, "1")}
		assert.ErrorIs(t, repo.CreateForControlPlane(ctx, &again, taken), utils.ErrBudgetAlreadyExists, taken)
	}
	assert.Len(t, budgetsByName(t, repo, ou), 1)
}

func TestBudgetRepository_GetUpdateDelete(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou := dbtest.Org(t, gdb)
	otherOrg := dbtest.Org(t, gdb)
	repo := NewBudgetRepository(gdb)
	createArtifact(t, gdb, ou, models.KindLLMProvider, "openai")

	b := models.Budget{OrgID: ou, Origin: models.BudgetOriginControlPlane, Configuration: budgetConfig("Team A", "100")}
	require.NoError(t, repo.CreateForControlPlane(ctx, &b, "team-a"))

	got, err := repo.Get(ctx, ou, "team-a")
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.ID)
	assert.Equal(t, "team-a", got.Name)
	assert.Equal(t, "100", got.Configuration.Limit.Amount)
	for _, missing := range []struct{ org, name string }{{ou, "nope"}, {otherOrg, "team-a"}, {ou, "openai"}} {
		_, err = repo.Get(ctx, missing.org, missing.name)
		assert.ErrorIs(t, err, utils.ErrBudgetNotFound, missing.name)
	}

	got.Configuration.Limit.Amount = "150"
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.Update(ctx, got))
	again, err := repo.Get(ctx, ou, "team-a")
	require.NoError(t, err)
	assert.Equal(t, "150", again.Configuration.Limit.Amount)
	assert.True(t, again.UpdatedAt.After(again.CreatedAt), "the artifact's updated_at moves")

	assert.ErrorIs(t, repo.Delete(ctx, otherOrg, "team-a"), utils.ErrBudgetNotFound)
	assert.ErrorIs(t, repo.Delete(ctx, ou, "openai"), utils.ErrBudgetNotFound, "only budgets are deleted")
	require.NoError(t, repo.Delete(ctx, ou, "team-a"))
	_, err = repo.Get(ctx, ou, "team-a")
	assert.ErrorIs(t, err, utils.ErrBudgetNotFound)
	var rows int64
	require.NoError(t, gdb.Model(&models.Budget{}).Where("id = ?", b.ID).Count(&rows).Error)
	assert.Zero(t, rows, "the row goes with the artifact")
}
