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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/wso2/agent-manager/agent-manager-service/middleware"
	"github.com/wso2/agent-manager/agent-manager-service/middleware/jwtassertion"
	"github.com/wso2/agent-manager/agent-manager-service/middleware/logger"
	"github.com/wso2/agent-manager/agent-manager-service/services"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// maxBudgetRequestBytes bounds a budget request body.
const maxBudgetRequestBytes = 1 << 20

// BudgetController serves the budgets API (/orgs/{orgName}/budgets).
type BudgetController interface {
	CreateBudget(w http.ResponseWriter, r *http.Request)
	ImportBudgets(w http.ResponseWriter, r *http.Request)
	ListBudgets(w http.ResponseWriter, r *http.Request)
	GetBudget(w http.ResponseWriter, r *http.Request)
	GetBudgetUsage(w http.ResponseWriter, r *http.Request)
	UpdateBudget(w http.ResponseWriter, r *http.Request)
	DeleteBudget(w http.ResponseWriter, r *http.Request)
}

// PathParamBudgetName is the budget path parameter.
const PathParamBudgetName = "budgetName"

type budgetController struct {
	budgets services.BudgetService
}

// NewBudgetController creates a BudgetController.
func NewBudgetController(budgets services.BudgetService) BudgetController {
	return &budgetController{budgets: budgets}
}

// CreateBudget handles POST /orgs/{orgName}/budgets.
func (c *budgetController) CreateBudget(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBudgetKind(w, r)
	if !ok {
		return
	}
	createdBy := ""
	if claims := jwtassertion.GetTokenClaims(r.Context()); claims != nil {
		createdBy = claims.Sub
	}
	resp, err := c.budgets.Create(r.Context(), middleware.OUIDFromRequest(r), createdBy, req)
	if err != nil {
		writeBudgetError(w, r, err, "Failed to create budget")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusCreated, resp)
}

// ImportBudgets handles POST /orgs/{orgName}/budgets/import: a CSV file, one budget per
// row, saved all or none (Interfaces 2.5). 200 when every row passed, 400 with the same
// result when a row failed, 400 INVALID_CSV when the file cannot be read.
func (c *budgetController) ImportBudgets(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "text/csv" {
		utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "Send the file as text/csv.", "", utils.ErrCodeInvalidCSV)
		return
	}
	dryRun := false
	if raw := r.URL.Query().Get("dryRun"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "dryRun must be true or false.", err.Error(), utils.ErrCodeValidation)
			return
		}
		dryRun = v
	}
	file, err := io.ReadAll(http.MaxBytesReader(w, r.Body, services.MaxBudgetImportBytes))
	if err != nil {
		utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "The file must be at most 1 MB.", err.Error(), utils.ErrCodeInvalidCSV)
		return
	}
	createdBy := ""
	if claims := jwtassertion.GetTokenClaims(r.Context()); claims != nil {
		createdBy = claims.Sub
	}
	result, err := c.budgets.Import(r.Context(), middleware.OUIDFromRequest(r), createdBy, bytes.NewReader(file), dryRun,
		r.URL.Query().Get("onExisting"))
	if err != nil {
		writeBudgetError(w, r, err, "Failed to import budgets")
		return
	}
	status := http.StatusOK
	if result.Summary.Error > 0 {
		status = http.StatusBadRequest
	}
	utils.WriteSuccessResponse(w, status, result)
}

// ListBudgets handles GET /orgs/{orgName}/budgets.
func (c *budgetController) ListBudgets(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, ok := budgetQueryInt(w, query.Get("limit"), "limit")
	if !ok {
		return
	}
	offset, ok := budgetQueryInt(w, query.Get("offset"), "offset")
	if !ok {
		return
	}
	resp, err := c.budgets.List(r.Context(), middleware.OUIDFromRequest(r), query.Get("projectId"), query["filter"], limit, offset)
	if err != nil {
		writeBudgetError(w, r, err, "Failed to list budgets")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// budgetQueryInt reads an optional integer query parameter; 0 when absent.
func budgetQueryInt(w http.ResponseWriter, raw, name string) (int32, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, name+" must be a whole number.", err.Error(), utils.ErrCodeValidation)
		return 0, false
	}
	return int32(v), true
}

// GetBudget handles GET /orgs/{orgName}/budgets/{budgetName}.
func (c *budgetController) GetBudget(w http.ResponseWriter, r *http.Request) {
	resp, err := c.budgets.Get(r.Context(), middleware.OUIDFromRequest(r), r.PathValue(PathParamBudgetName))
	if err != nil {
		writeBudgetError(w, r, err, "Failed to get budget")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// GetBudgetUsage handles GET /orgs/{orgName}/budgets/{budgetName}/budget-usage: each
// allowance's spend this period, highest first.
func (c *budgetController) GetBudgetUsage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, ok := budgetQueryInt(w, query.Get("limit"), "limit")
	if !ok {
		return
	}
	offset, ok := budgetQueryInt(w, query.Get("offset"), "offset")
	if !ok {
		return
	}
	resp, err := c.budgets.Usage(r.Context(), middleware.OUIDFromRequest(r), r.PathValue(PathParamBudgetName), limit, offset)
	if err != nil {
		writeBudgetError(w, r, err, "Failed to read budget usage")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// UpdateBudget handles PUT /orgs/{orgName}/budgets/{budgetName}: the whole Budget kind,
// with the fields fixed after creation unchanged. Unknown fields are rejected.
func (c *budgetController) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBudgetKind(w, r)
	if !ok {
		return
	}
	resp, err := c.budgets.Update(r.Context(), middleware.OUIDFromRequest(r), r.PathValue(PathParamBudgetName), req)
	if err != nil {
		writeBudgetError(w, r, err, "Failed to update budget")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// DeleteBudget handles DELETE /orgs/{orgName}/budgets/{budgetName}.
func (c *budgetController) DeleteBudget(w http.ResponseWriter, r *http.Request) {
	if err := c.budgets.Delete(r.Context(), middleware.OUIDFromRequest(r), r.PathValue(PathParamBudgetName)); err != nil {
		writeBudgetError(w, r, err, "Failed to delete budget")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeBudgetError answers a refused request with its code and field (Interfaces 2.3).
func writeBudgetError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	var be *services.BudgetError
	if errors.As(err, &be) {
		status := http.StatusBadRequest
		switch be.Code {
		case utils.ErrCodeBudgetAlreadyExists, utils.ErrCodeBudgetReadOnly:
			status = http.StatusConflict
		case utils.ErrCodeBudgetNotFound:
			status = http.StatusNotFound
		}
		body := spec.NewErrorResponse(be.Message, be.Code)
		if be.Field != "" {
			body.AdditionalData = map[string]interface{}{"field": be.Field}
		}
		utils.WriteSuccessResponse(w, status, body)
		return
	}
	logger.GetLogger(r.Context()).Error(fallback, "error", err)
	utils.WriteErrorResponseWithReason(w, http.StatusInternalServerError, fallback, "", utils.ErrCodeInternalError)
}

// decodeBudgetKind reads the Budget kind of a create or update, as JSON or, like the
// gateway's API, as YAML (application/yaml). Unknown fields are rejected; status is ignored.
func decodeBudgetKind(w http.ResponseWriter, r *http.Request) (spec.Budget, bool) {
	var kind spec.Budget
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBudgetRequestBytes))
	if err != nil {
		utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "The budget must be at most 1 MB.", err.Error(), utils.ErrCodeValidation)
		return kind, false
	}
	if isYAMLRequest(r) {
		// YAML is read into plain values and re-encoded, so the JSON field names and checks apply.
		var doc any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "Invalid budget: the YAML cannot be read.", err.Error(), utils.ErrCodeValidation)
			return kind, false
		}
		if body, err = json.Marshal(doc); err != nil {
			utils.WriteErrorResponseWithReason(w, http.StatusBadRequest, "Invalid budget: the YAML cannot be read.", err.Error(), utils.ErrCodeValidation)
			return kind, false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&kind); err != nil {
		utils.WriteErrorResponseWithReason(w, http.StatusBadRequest,
			"Invalid budget. Send a Budget kind: apiVersion, kind, metadata and spec.", err.Error(), utils.ErrCodeValidation)
		return kind, false
	}
	kind.Status = nil
	return kind, true
}

// isYAMLRequest reports whether the request body is YAML.
func isYAMLRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	switch mediaType {
	case "application/yaml", "application/x-yaml", "text/yaml":
		return true
	}
	return false
}
