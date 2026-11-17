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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// importCSV is the design's example (Interfaces 2.5), in the test environment's names.
const importCSV = `name,projectId,displayName,amount,period,action,blockAt,llmProvider,llmProviderAllocation,project,agentAllocation,apiKey,attributes,exemptions,thresholds,channels
wso2-monthly,,WSO2 monthly ceiling,10000,month,block,99,,,,,,,"[{""type"": ""project"", ""id"": ""api-platform""}]",50;80;100,finance-email
api-platform-agents,api-platform,"API Platform, per agent",100,month,block,,openai,,api-platform,separate,,,"[{""type"": ""agent"", ""project"": ""api-platform"", ""id"": ""hotel-booking""}]",80;100,platform-slack
api-platform-cap,,API Platform cap,500,month,block,,,separate,api-platform,,,,,80,finance-email
rnd-per-user,,"R&D, per user",20,day,block,,,,,,,"[{""expression"": ""'research' in request.Auth.Claims[\""groups\""]""}, {""allocation"": ""separate"", ""expression"": ""request.Auth.Claims[\""email\""]""}]",,80;100,finance-email
ci-key-daily,,,20,day,block,,,,,,ci-pipeline,,,,
`

func importBudgets(t *testing.T, env *budgetTestEnv, file string, dryRun bool) *spec.BudgetImportResult {
	t.Helper()
	result, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(file), dryRun, "")
	require.NoError(t, err)
	return result
}

func TestBudgetImport_CreatesEveryRow(t *testing.T) {
	env := newBudgetTestEnv(t)
	result := importBudgets(t, env, importCSV, false)

	assert.True(t, result.Applied)
	assert.False(t, result.DryRun)
	assert.Equal(t, spec.BudgetImportSummary{Valid: 5, Created: 5}, result.Summary)
	require.Len(t, result.Results, 5)
	assert.Equal(t, int32(2), result.Results[0].Row, "the header is row 1")
	assert.Equal(t, "wso2-monthly", *result.Results[0].Name)
	assert.Empty(t, result.Results[0].Errors)

	require.Len(t, env.created, 5)
	byName := map[string]models.Budget{}
	for _, b := range env.created {
		byName[b.Name] = b
		assert.Equal(t, "alice", b.CreatedBy)
		assert.Equal(t, models.BudgetOriginControlPlane, b.Origin)
	}

	org := byName["wso2-monthly"].Configuration
	assert.Equal(t, int32(99), *org.Limit.BlockAt)
	assert.Equal(t, []int32{50, 80, 100}, org.Warnings.Thresholds)
	assert.Equal(t, []string{"finance-email"}, org.Warnings.Channels, "channels are named in the file and stored by name")
	assert.Nil(t, byName["wso2-monthly"].ProjectID)

	agents := byName["api-platform-agents"]
	require.NotNil(t, agents.ProjectID, "projectId makes it the project's budget")
	assert.Equal(t, budgetTestProjectID, agents.ProjectID.String())
	stored, err := json.Marshal(agents.Configuration.Consumers)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"type": "project", "allocation": "shared", "id": "api-platform"},
		{"type": "agent", "allocation": "separate"}
	]`, string(stored), "the project and agentAllocation columns, as given")
	assert.Equal(t, "openai", *agents.Configuration.Resource.ID)
	assert.Equal(t, "hotel-booking", *agents.Configuration.Exemptions[0].ID)

	limitedProject := byName["api-platform-cap"]
	assert.Nil(t, limitedProject.ProjectID, "the project column is a consumer, not the owner")
	assert.Equal(t, "separate", limitedProject.Configuration.Resource.Allocation)
	assert.Equal(t, "api-platform", *limitedProject.Configuration.Consumers[0].ID)

	users := byName["rnd-per-user"].Configuration.Consumers
	require.Len(t, users, 2)
	assert.Equal(t, "attribute", users[0].Type, "attribute is the default type in the attributes column")
	assert.Equal(t, "separate", users[1].Allocation)

	key := byName["ci-key-daily"]
	assert.Equal(t, "ci-key-daily", key.Configuration.DisplayName, "no displayName: the name is shown")
	assert.Equal(t, "apiKey", key.Configuration.Consumers[0].Type)
	assert.Nil(t, key.Configuration.Warnings)

	assert.Len(t, env.sender.sent, 5, "each saved budget goes to the gateways")
}

func TestBudgetImport_DryRunSavesNothing(t *testing.T) {
	env := newBudgetTestEnv(t)
	result := importBudgets(t, env, importCSV, true)
	assert.True(t, result.DryRun)
	assert.False(t, result.Applied)
	assert.Equal(t, int32(5), result.Summary.Valid)
	assert.Empty(t, env.budgets.CreateManyForControlPlaneCalls())
	assert.Empty(t, env.sender.sent)
}

func TestBudgetImport_ReportsEachRowsErrorsAndSavesNothing(t *testing.T) {
	env := newBudgetTestEnv(t)
	createTestBudget(t, env, "existing")
	env.created, env.sender.sent = nil, nil
	file := `name,amount,period,action,blockAt,projectId,project,agent,exemptions,thresholds,channels
good,100,month,block,,,,,,,
bad-amount,ten,month,block,,,,,,,
bad-block-at,100,month,block,most,,,,,,
no-such-agent,100,month,block,,,api-platform,ghost,,,
no-such-channel,100,month,block,,,,,,80,pagerduty
bad-json,100,month,block,,,,,[{,,
other-project,100,month,block,,api-platform,support,,,,
existing,100,month,block,,,,,,,
good,50,day,allow,,,,,,,
`
	result := importBudgets(t, env, file, false)
	assert.False(t, result.Applied)
	assert.Equal(t, spec.BudgetImportSummary{Valid: 1, Error: 8, Created: 1}, result.Summary)
	assert.Empty(t, env.budgets.CreateManyForControlPlaneCalls(), "one bad row and nothing is saved")
	assert.Empty(t, env.sender.sent)

	type cell struct{ column, code string }
	got := map[int32]cell{}
	for _, r := range result.Results {
		if len(r.Errors) > 0 {
			column := ""
			if r.Errors[0].Column != nil {
				column = *r.Errors[0].Column
			}
			got[r.Row] = cell{column, r.Errors[0].Code}
			assert.NotEmpty(t, r.Errors[0].Message)
		}
	}
	assert.Equal(t, map[int32]cell{
		3:  {"amount", utils.ErrCodeValidation},
		4:  {"blockAt", utils.ErrCodeValidation},
		5:  {"agent", utils.ErrCodeReferenceNotFound},
		6:  {"channels", utils.ErrCodeChannelNotAvailable},
		7:  {"exemptions", utils.ErrCodeValidation},
		8:  {"project", utils.ErrCodeValidation},
		9:  {"name", utils.ErrCodeBudgetAlreadyExists},
		10: {"name", utils.ErrCodeBudgetAlreadyExists},
	}, got)
}

func TestBudgetImport_RefusesAFileItCannotRead(t *testing.T) {
	many := "name,amount,period,action\n" + strings.Repeat("x,1,day,allow\n", maxBudgetImportRows+1)
	cases := map[string]string{
		"empty":            "",
		"header only":      "name,amount,period,action\n",
		"unknown column":   "name,amount,period,action,owner\nx,1,day,allow,me\n",
		"repeated column":  "name,amount,period,action,name\nx,1,day,allow,y\n",
		"missing required": "name,amount,period\nx,1,day\n",
		"short row":        "name,amount,period,action\nx,1,day\n",
		"bad quotes":       "name,amount,period,action\n\"x,1,day,allow\n",
		"too many rows":    many,
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			env := newBudgetTestEnv(t)
			_, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(file), false, "")
			assert.Equal(t, utils.ErrCodeInvalidCSV, budgetErrorCode(t, err))
			assert.Empty(t, env.created)
		})
	}
}

func TestBudgetImport_ReadsAnyColumnOrderAndAByteOrderMark(t *testing.T) {
	env := newBudgetTestEnv(t)
	result := importBudgets(t, env, "\xEF\xBB\xBFaction, period ,amount,name\nallow,day,5,tiny\n", false)
	require.True(t, result.Applied)
	require.Len(t, env.created, 1)
	b := env.created[0]
	assert.Equal(t, "tiny", b.Name)
	assert.Equal(t, "5", b.Configuration.Limit.Amount)
	assert.Equal(t, "allow", b.Configuration.Limit.Action)
}

func TestBudgetImport_ANameTakenWhileSavingSavesNothing(t *testing.T) {
	env := newBudgetTestEnv(t)
	env.budgets.CreateManyForControlPlaneFunc = func(context.Context, []*models.Budget, []string, []*models.Budget) error {
		return &repositories.BudgetNameTakenError{Name: "second"}
	}
	result := importBudgets(t, env, "name,amount,period,action\nfirst,1,day,allow\nsecond,1,day,allow\n", false)
	assert.False(t, result.Applied)
	assert.Equal(t, spec.BudgetImportSummary{Valid: 1, Error: 1, Created: 1}, result.Summary)
	assert.Equal(t, "name", *result.Results[1].Errors[0].Column)
	assert.Equal(t, utils.ErrCodeBudgetAlreadyExists, result.Results[1].Errors[0].Code)
	assert.Empty(t, env.sender.sent)
}

func TestBudgetImport_OrgIDMustBeThisOrganization(t *testing.T) {
	env := newBudgetTestEnv(t)
	result := importBudgets(t, env, "name,orgId,amount,period,action\nmine,wso2,1,day,allow\ntheirs,acme,1,day,allow\n", true)
	assert.Empty(t, result.Results[0].Errors)
	require.Len(t, result.Results[1].Errors, 1)
	assert.Equal(t, "orgId", *result.Results[1].Errors[0].Column)
}

func TestBudgetService_DisplayNameDefaultsToTheName(t *testing.T) {
	env := newBudgetTestEnv(t)
	req := budgetTestRequest()
	req.DisplayName = nil
	resp, err := env.create(context.Background(), budgetTestOrg, "alice", req)
	require.NoError(t, err)
	assert.Equal(t, req.Name, *resp.Spec.DisplayName)

	updated, err := env.update(context.Background(), budgetTestOrg, req.Name, budgetUpdateRequest{
		DisplayName: strPtr(" "), Limit: budgetUpdateLimit{Amount: "100.00", Action: "block"},
	})
	require.NoError(t, err)
	assert.Equal(t, req.Name, *updated.Spec.DisplayName)
}

// existingBudgetEnv has a control-plane budget "team-a" ($100 a month, project api-platform
// consumer) and a gateway's budget "from-gateway".
func existingBudgetEnv(t *testing.T) *budgetTestEnv {
	t.Helper()
	env := newBudgetTestEnv(t)
	_, err := env.create(context.Background(), budgetTestOrg, "alice", budgetRequest{
		Name: "team-a", DisplayName: strPtr("Team A"),
		Limit:     spec.BudgetLimit{Amount: "100", Period: "month", Action: "block"},
		Consumers: []spec.BudgetConsumer{{Type: "project", Id: strPtr("api-platform")}},
	})
	require.NoError(t, err)
	gw := env.store["team-a"]
	gw.Name, gw.Origin = "from-gateway", models.BudgetOriginGateway
	env.store["from-gateway"] = gw
	env.created, env.sender.sent = nil, nil
	return env
}

const existingCSV = "name,displayName,amount,period,action,project,thresholds,channels\n" +
	"team-a,Team A (raised),250,month,allow,api-platform,80,finance-email\n" +
	"new-one,,10,day,block,,,\n"

func TestBudgetImport_AnExistingNameFailsWithAHintByDefault(t *testing.T) {
	env := existingBudgetEnv(t)
	result := importBudgets(t, env, existingCSV, false)
	assert.False(t, result.Applied)
	require.Len(t, result.Results[0].Errors, 1)
	e := result.Results[0].Errors[0]
	assert.Equal(t, utils.ErrCodeBudgetAlreadyExists, e.Code)
	assert.Equal(t, "name", *e.Column)
	assert.Contains(t, e.Message, "onExisting=update")
	assert.Contains(t, e.Message, "onExisting=skip")
	assert.Empty(t, env.created, "nothing is saved")
}

func TestBudgetImport_SkipLeavesAnExistingBudgetAsItIs(t *testing.T) {
	env := existingBudgetEnv(t)
	result, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(existingCSV), false, OnExistingSkip)
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, spec.BudgetImportSummary{Valid: 2, Created: 1, Skipped: 1}, result.Summary)
	assert.Equal(t, "skip", *result.Results[0].Action)
	assert.Equal(t, "create", *result.Results[1].Action)
	assert.Equal(t, "100", env.store["team-a"].Configuration.Limit.Amount, "left as it is")
	assert.Equal(t, []string{"created:new-one:active"}, env.sender.sent)
}

func TestBudgetImport_UpdateReplacesTheEditableFields(t *testing.T) {
	env := existingBudgetEnv(t)
	result, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(existingCSV), false, OnExistingUpdate)
	require.NoError(t, err)
	require.True(t, result.Applied, "%+v", result.Results)
	assert.Equal(t, spec.BudgetImportSummary{Valid: 2, Created: 1, Updated: 1}, result.Summary)
	assert.Equal(t, "update", *result.Results[0].Action)
	updated := env.store["team-a"].Configuration
	assert.Equal(t, "250", updated.Limit.Amount)
	assert.Equal(t, "allow", updated.Limit.Action)
	assert.Equal(t, "Team A (raised)", updated.DisplayName)
	assert.Equal(t, []string{"finance-email"}, updated.Warnings.Channels)
	assert.Equal(t, "active", updated.Status, "the status stays")
	assert.ElementsMatch(t, []string{"updated:team-a:active", "created:new-one:active"}, env.sender.sent)
}

func TestBudgetImport_UpdateRefusesFixedChangesAndGatewayBudgets(t *testing.T) {
	env := existingBudgetEnv(t)
	file := "name,amount,period,action,project\n" +
		"team-a,250,day,allow,api-platform\n" +
		"from-gateway,250,month,allow,api-platform\n" +
		"openai,1,day,allow,\n"
	result, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(file), true, OnExistingUpdate)
	require.NoError(t, err)
	require.Equal(t, int32(3), result.Summary.Error)

	period := result.Results[0].Errors[0]
	assert.Equal(t, "period", *period.Column, "the period is fixed after creation")
	assert.Equal(t, utils.ErrCodeValidation, period.Code)
	assert.Equal(t, utils.ErrCodeBudgetReadOnly, result.Results[1].Errors[0].Code, "a gateway's budget is changed on the gateway")
	other := result.Results[2].Errors[0]
	assert.Equal(t, utils.ErrCodeBudgetAlreadyExists, other.Code, "a name held by another kind is never overwritten")
	assert.Contains(t, other.Message, "another resource")
}

func TestBudgetImport_RefusesAnUnknownOnExisting(t *testing.T) {
	env := newBudgetTestEnv(t)
	_, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(existingCSV), true, "overwrite")
	assert.Equal(t, utils.ErrCodeValidation, budgetErrorCode(t, err))
}

// A row lists every column that is wrong on its own, not only the first.
func TestBudgetImport_ListsEveryBadColumnOfARow(t *testing.T) {
	env := newBudgetTestEnv(t)
	file := "name,amount,period,action,currency,agentAllocation,blockAt,thresholds\n" +
		"Bad Row,0100,decade,maybe,EUR,each,150,80;5000\n"
	result, err := env.svc.Import(context.Background(), budgetTestOrg, "alice", strings.NewReader(file), true, "")
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	columns := map[string]bool{}
	for _, e := range result.Results[0].Errors {
		columns[e.GetColumn()] = true
	}
	for _, c := range []string{"name", "amount", "period", "action", "currency", "agentAllocation", "blockAt", "thresholds"} {
		assert.True(t, columns[c], "column %s is reported", c)
	}
	assert.Empty(t, env.created, "nothing is stored")
}
