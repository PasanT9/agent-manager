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

package api

import (
	"net/http"

	"github.com/wso2/agent-manager/agent-manager-service/controllers"
	"github.com/wso2/agent-manager/agent-manager-service/middleware"
	"github.com/wso2/agent-manager/agent-manager-service/rbac"
	"github.com/wso2/agent-manager/agent-manager-service/services"
)

func registerBudgetRoutes(rr *middleware.RouteRegistrar, controller controllers.BudgetController) {
	base := "/orgs/{orgName}/budgets"
	rr.HandleFuncWithValidationAndAuthz(route("GET", base), rbac.BudgetRead, controller.ListBudgets)
	rr.HandleFuncWithValidationAndAuthz(route("POST", base), rbac.BudgetCreate, controller.CreateBudget)
	rr.HandleFuncWithValidationAndAuthz(route("POST", base+"/import"), rbac.BudgetCreate, importBudgets(controller))
	rr.HandleFuncWithValidationAndAuthz(route("GET", base+"/{budgetName}"), rbac.BudgetRead, controller.GetBudget)
	rr.HandleFuncWithValidationAndAuthz(route("GET", base+"/{budgetName}/budget-usage"), rbac.BudgetRead, controller.GetBudgetUsage)
	rr.HandleFuncWithValidationAndAuthz(route("PUT", base+"/{budgetName}"), rbac.BudgetUpdate, controller.UpdateBudget)
	rr.HandleFuncWithValidationAndAuthz(route("DELETE", base+"/{budgetName}"), rbac.BudgetDelete, controller.DeleteBudget)
}

// importBudgets is the import handler: an import that may replace existing budgets
// (onExisting=update) also needs budget:update, dry run or not, so creating budgets never
// lets a caller change ones that exist.
func importBudgets(controller controllers.BudgetController) http.HandlerFunc {
	withUpdate := middleware.RequirePermission(rbac.BudgetUpdate)(controller.ImportBudgets)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("onExisting") == services.OnExistingUpdate {
			withUpdate(w, r)
			return
		}
		controller.ImportBudgets(w, r)
	}
}
