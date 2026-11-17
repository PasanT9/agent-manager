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

package controllers

import (
	"net/http"

	"github.com/wso2/agent-manager/agent-manager-service/middleware"
	"github.com/wso2/agent-manager/agent-manager-service/services"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// CostController serves the costs API (/orgs/{orgName}/costs).
type CostController interface {
	GetCosts(w http.ResponseWriter, r *http.Request)
}

type costController struct {
	costs services.CostService
}

// NewCostController creates a CostController.
func NewCostController(costs services.CostService) CostController {
	return &costController{costs: costs}
}

// GetCosts handles GET /orgs/{orgName}/costs: the kept months' cost per groupBy value.
func (c *costController) GetCosts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	resp, err := c.costs.Costs(r.Context(), middleware.OUIDFromRequest(r), middleware.OrgHandleFromRequest(r),
		query.Get("groupBy"), query["filter"])
	if err != nil {
		writeBudgetError(w, r, err, "Failed to read costs")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}
