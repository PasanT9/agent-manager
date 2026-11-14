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

func budgetRow(ouID string, gatewayID *uuid.UUID, name, spec string) models.Budget {
	ts := time.Now().UTC().Truncate(time.Microsecond)
	b := models.Budget{
		ID: uuid.Must(uuid.NewV7()), OUID: ouID, Name: name, Origin: models.BudgetOriginControlPlane,
		Configuration: json.RawMessage(`{"kind":"Budget","metadata":{"name":"` + name + `"},"spec":` + spec + `}`),
		CreatedAt:     ts, UpdatedAt: ts,
	}
	if gatewayID != nil {
		b.Origin, b.GatewayID, b.SyncedAt = models.BudgetOriginGateway, gatewayID, &ts
	}
	return b
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

func TestBudgetRepository_GatewayBudgets(t *testing.T) {
	gdb := db.GetDB()
	ctx := context.Background()
	ou := dbtest.Org(t, gdb)
	gw := dbtest.Gateway(t, gdb, ou, "egress").UUID
	other := dbtest.Gateway(t, gdb, ou, "egress").UUID
	repo := NewBudgetRepository(gdb)
	save := func(b models.Budget) (models.Budget, bool) {
		t.Helper()
		stored, err := repo.SaveForGateway(ctx, &b)
		require.NoError(t, err)
		return b, stored
	}

	// A control-plane budget and another gateway's budget share the table.
	cp := budgetRow(ou, nil, "org-wide", `{"amount":"1000"}`)
	require.NoError(t, gdb.Create(&cp).Error)
	_, stored := save(budgetRow(ou, &other, "team-z", `{"amount":"5"}`))
	require.True(t, stored)

	a, stored := save(budgetRow(ou, &gw, "team-a", `{"amount":"100"}`))
	require.True(t, stored)
	_, stored = save(budgetRow(ou, &gw, "team-b", `{"amount":"200"}`))
	require.True(t, stored)
	_, stored = save(budgetRow(ou, &gw, "org-wide", `{"amount":"1"}`))
	assert.False(t, stored, "a name the control plane holds is not taken")
	_, stored = save(budgetRow(ou, &gw, "team-z", `{"amount":"1"}`))
	assert.False(t, stored, "a name another gateway holds is not taken")
	all := budgetsByName(t, repo, ou)
	assert.Len(t, all, 4)
	assert.JSONEq(t, `{"kind":"Budget","metadata":{"name":"org-wide"},"spec":{"amount":"1000"}}`, string(all["org-wide"].Configuration))
	assert.Equal(t, other, *all["team-z"].GatewayID)

	// A later push of team-a, with a fresh ID, updates the stored budget and keeps its ID.
	again, stored := save(budgetRow(ou, &gw, "team-a", `{"amount":"150"}`))
	require.True(t, stored)
	assert.Equal(t, a.ID, again.ID, "the control plane's ID is kept across pushes")
	all = budgetsByName(t, repo, ou)
	assert.Equal(t, a.ID, all["team-a"].ID)
	assert.JSONEq(t, string(again.Configuration), string(all["team-a"].Configuration))

	// A deletion removes only that gateway's budget of the name.
	require.NoError(t, repo.DeleteForGateway(ctx, ou, other, []string{"team-a"}))
	require.NoError(t, repo.DeleteForGateway(ctx, ou, gw, []string{"team-b", "unknown"}))
	all = budgetsByName(t, repo, ou)
	assert.Contains(t, all, "team-a")
	assert.NotContains(t, all, "team-b")

	// A complete list removes the gateway's budgets it does not name, and nothing else.
	_, _ = save(budgetRow(ou, &gw, "team-c", `{"amount":"300"}`))
	require.NoError(t, repo.DeleteForGatewayExcept(ctx, ou, gw, []string{"team-c"}))
	all = budgetsByName(t, repo, ou)
	assert.ElementsMatch(t, []string{"org-wide", "team-z", "team-c"}, keys(all))
	require.NoError(t, repo.DeleteForGatewayExcept(ctx, ou, gw, nil))
	assert.ElementsMatch(t, []string{"org-wide", "team-z"}, keys(budgetsByName(t, repo, ou)))
}

func keys(m map[string]models.Budget) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
