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
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// CSV import of budgets (Interfaces 2.5): one budget per row, the same fields and checks
// as POST /budgets, saved all or none.

// MaxBudgetImportBytes is the largest CSV file an import takes.
const MaxBudgetImportBytes = 1 << 20

// maxBudgetImportRows is the most budgets one import creates: they are saved in one transaction.
const maxBudgetImportRows = 500

// CSV columns. Lists use ';'; attributes and exemptions are JSON arrays of the payload's objects.
const (
	csvName               = "name"
	csvOrgID              = "orgId"
	csvProjectID          = "projectId"
	csvDisplayName        = "displayName"
	csvDescription        = "description"
	csvAmount             = "amount"
	csvCurrency           = "currency"
	csvPeriod             = "period"
	csvAction             = "action"
	csvBlockAt            = "blockAt"
	csvProvider           = "provider"
	csvProviderAllocation = "providerAllocation"
	csvProject            = "project"
	csvProjectAllocation  = "projectAllocation"
	csvAgent              = "agent"
	csvAgentAllocation    = "agentAllocation"
	csvAPIKey             = "apiKey"
	csvAPIKeyAllocation   = "apiKeyAllocation"
	csvAttributes         = "attributes"
	csvExemptions         = "exemptions"
	csvThresholds         = "thresholds"
	csvChannels           = "channels"
)

var (
	budgetImportColumns = []string{
		csvName, csvOrgID, csvProjectID, csvDisplayName, csvDescription,
		csvAmount, csvCurrency, csvPeriod, csvAction, csvBlockAt,
		csvProvider, csvProviderAllocation,
		csvProject, csvProjectAllocation, csvAgent, csvAgentAllocation, csvAPIKey, csvAPIKeyAllocation,
		csvAttributes, csvExemptions, csvThresholds, csvChannels,
	}
	budgetImportRequired = []string{csvName, csvAmount, csvPeriod, csvAction}

	// budgetConsumerColumns are the value and allocation columns of the named consumers,
	// in the order they become consumers.
	budgetConsumerColumns = []struct{ typ, value, allocation string }{
		{budgetTypeProject, csvProject, csvProjectAllocation},
		{budgetTypeAgent, csvAgent, csvAgentAllocation},
		{budgetTypeAPIKey, csvAPIKey, csvAPIKeyAllocation},
	}

	budgetFieldIndex = regexp.MustCompile(`^consumers\[(\d+)\]`)
	utf8BOM          = []byte{0xEF, 0xBB, 0xBF}
)

// invalidCSV refuses a file that cannot be read as a whole.
func invalidCSV(format string, args ...any) error {
	return &BudgetError{Code: utils.ErrCodeInvalidCSV, Message: fmt.Sprintf(format, args...)}
}

// What an import does with a row naming an existing budget (onExisting).
const (
	OnExistingFail   = "fail"
	OnExistingSkip   = "skip"
	OnExistingUpdate = "update"
)

// Row actions.
const (
	importCreate = "create"
	importUpdate = "update"
	importSkip   = "skip"
)

// importRow is one CSV row on its way to becoming a budget.
type importRow struct {
	row    int32
	name   string
	req    budgetRequest
	budget *models.Budget // the budget the row creates, or the stored budget it updates
	action string
	errs   []spec.BudgetImportRowError
}

func (r *importRow) fail(column, code, message string) {
	e := spec.BudgetImportRowError{Code: code, Message: message}
	if column != "" {
		e.Column = &column
	}
	r.errs = append(r.errs, e)
}

func (s *budgetService) Import(ctx context.Context, ouID, createdBy string, file io.Reader, dryRun bool, onExisting string) (*spec.BudgetImportResult, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	if onExisting == "" {
		onExisting = OnExistingFail
	}
	if onExisting != OnExistingFail && onExisting != OnExistingSkip && onExisting != OnExistingUpdate {
		return nil, invalidBudget("onExisting", "onExisting must be fail, skip or update.")
	}
	header, records, err := readBudgetCSV(file)
	if err != nil {
		return nil, err
	}
	// An empty orgId cell means the organization the file is imported into.
	orgName, err := s.orgHandle(ctx, ouID)
	if err != nil {
		return nil, err
	}

	rows := make([]*importRow, 0, len(records))
	firstRow := map[string]int32{}
	for i, record := range records {
		cells := make(map[string]string, len(header))
		for c, column := range header {
			cells[column] = strings.TrimSpace(record[c])
		}
		row := &importRow{row: int32(i + 2), name: cells[csvName]}
		rows = append(rows, row)
		row.req = budgetRequestFromCSV(cells, orgName, row)
		if row.name != "" {
			if first, seen := firstRow[row.name]; seen {
				row.fail(csvName, utils.ErrCodeBudgetAlreadyExists, fmt.Sprintf("Row %d already uses the name %s.", first, row.name))
			} else {
				firstRow[row.name] = row.row
			}
		}
		if len(row.errs) > 0 {
			continue
		}
		budget, err := s.prepareBudget(ctx, ouID, createdBy, row.req)
		var be *BudgetError
		switch {
		case errors.As(err, &be):
			row.fail(budgetCSVColumn(be.Field, row.req), be.Code, be.Message)
		case err != nil:
			return nil, err
		default:
			row.budget, row.action = budget, importCreate
		}
	}
	if err := s.resolveExisting(ctx, ouID, rows, onExisting); err != nil {
		return nil, err
	}

	result := importResult(rows, dryRun)
	if dryRun || result.Summary.Error > 0 {
		return result, nil
	}
	var creates, updates []*models.Budget
	var names []string
	for _, row := range rows {
		switch row.action {
		case importCreate:
			creates, names = append(creates, row.budget), append(names, row.name)
		case importUpdate:
			updates = append(updates, row.budget)
		}
	}
	if err := s.budgetRepo.CreateManyForControlPlane(ctx, creates, names, updates); err != nil {
		// Another request took a name after the check: nothing was saved.
		var taken *repositories.BudgetNameTakenError
		if !errors.As(err, &taken) {
			return nil, err
		}
		for _, row := range rows {
			if row.name == taken.Name {
				row.fail(csvName, utils.ErrCodeBudgetAlreadyExists, budgetNameTaken(row.name).Error())
			}
		}
		return importResult(rows, dryRun), nil
	}
	result.Applied = true
	for _, b := range creates {
		s.gateways.SendBudget(ctx, b, true)
	}
	for _, b := range updates {
		s.gateways.SendBudget(ctx, b, false)
		if s.warner != nil {
			// A lower limit or a new threshold may already be crossed.
			s.warner.CheckBudget(ctx, b)
		}
	}
	return result, nil
}

// readBudgetCSV reads the header and the rows, refusing a file that cannot be read as a
// whole: not CSV, unknown, repeated or missing required columns, no rows or too many.
func readBudgetCSV(file io.Reader) ([]string, [][]string, error) {
	br := bufio.NewReader(file)
	if bom, _ := br.Peek(len(utf8BOM)); bytes.Equal(bom, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	r := csv.NewReader(br)
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, invalidCSV("The file is empty: it needs a header row and at least one budget.")
	}
	if err != nil {
		return nil, nil, invalidCSV("The file is not valid CSV: %v.", err)
	}
	seen := map[string]bool{}
	for i, column := range header {
		column = strings.TrimSpace(column)
		header[i] = column
		if !containsString(budgetImportColumns, column) {
			return nil, nil, invalidCSV("Unknown column %q. Columns: %s.", column, strings.Join(budgetImportColumns, ", "))
		}
		if seen[column] {
			return nil, nil, invalidCSV("Column %s appears twice.", column)
		}
		seen[column] = true
	}
	for _, column := range budgetImportRequired {
		if !seen[column] {
			return nil, nil, invalidCSV("Column %s is required.", column)
		}
	}
	var records [][]string
	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, invalidCSV("The file is not valid CSV: %v.", err)
		}
		if len(records) == maxBudgetImportRows {
			return nil, nil, invalidCSV("The file has more than %d budgets.", maxBudgetImportRows)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, nil, invalidCSV("The file has no budgets: add a row under the header.")
	}
	return header, records, nil
}

// budgetRequestFromCSV maps a row onto the payload of POST /budgets. Cells that cannot be
// read (a number, JSON) fail their column on row.
func budgetRequestFromCSV(cells map[string]string, orgName string, row *importRow) budgetRequest {
	optional := func(column string) *string {
		if v := cells[column]; v != "" {
			return &v
		}
		return nil
	}
	req := budgetRequest{
		Name: cells[csvName], ProjectId: optional(csvProjectID),
		DisplayName: optional(csvDisplayName), Description: optional(csvDescription),
		Limit: spec.BudgetLimit{
			Amount: cells[csvAmount], Currency: optional(csvCurrency), Period: cells[csvPeriod], Action: cells[csvAction],
		},
	}
	if v := strings.TrimSpace(cells[csvOrgID]); v != "" && v != orgName {
		// The organization is the one imported into, as for a Budget kind.
		row.fail(csvOrgID, utils.ErrCodeValidation, fmt.Sprintf("orgId must be this organization, %s.", orgName))
	}
	if v := cells[csvBlockAt]; v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			row.fail(csvBlockAt, utils.ErrCodeValidation, "blockAt must be a whole percentage, 1-100.")
		}
		blockAt := int32(n)
		req.Limit.BlockAt = &blockAt
	}
	if cells[csvProvider] != "" || cells[csvProviderAllocation] != "" {
		req.Resource = &spec.BudgetResource{Type: budgetTypeLLMProvider, Id: optional(csvProvider), Allocation: optional(csvProviderAllocation)}
	}
	req.Consumers = []spec.BudgetConsumer{}
	for _, c := range budgetConsumerColumns {
		if cells[c.value] != "" || cells[c.allocation] != "" {
			req.Consumers = append(req.Consumers, spec.BudgetConsumer{Type: c.typ, Id: optional(c.value), Allocation: optional(c.allocation)})
		}
	}
	if v := cells[csvAttributes]; v != "" {
		var attributes []spec.BudgetConsumer
		if err := decodeCSVJSON(v, &attributes); err != nil {
			row.fail(csvAttributes, utils.ErrCodeValidation, "attributes must be a JSON array of attribute consumers: "+err.Error())
		}
		for _, a := range attributes {
			if a.Type == "" {
				a.Type = budgetTypeAttribute
			}
			if a.Type != budgetTypeAttribute {
				row.fail(csvAttributes, utils.ErrCodeValidation, "attributes takes attribute consumers only; use the project, agent and apiKey columns.")
				break
			}
			req.Consumers = append(req.Consumers, a)
		}
	}
	req.Exemptions = []spec.BudgetExemption{}
	if v := cells[csvExemptions]; v != "" {
		if err := decodeCSVJSON(v, &req.Exemptions); err != nil {
			row.fail(csvExemptions, utils.ErrCodeValidation, "exemptions must be a JSON array of exemptions: "+err.Error())
		}
	}
	if cells[csvThresholds] != "" || cells[csvChannels] != "" {
		warnings := &spec.BudgetWarnings{Thresholds: []int32{}, Channels: csvList(cells[csvChannels])}
		for _, v := range csvList(cells[csvThresholds]) {
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				row.fail(csvThresholds, utils.ErrCodeValidation, "thresholds must be whole percentages separated by ';', e.g. 80;100.")
				break
			}
			warnings.Thresholds = append(warnings.Thresholds, int32(n))
		}
		req.Warnings = warnings
	}
	return req
}

// csvList splits a ';' list, dropping empty items.
func csvList(v string) []string {
	out := []string{}
	for _, item := range strings.Split(v, ";") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// decodeCSVJSON reads a JSON cell strictly: unknown fields are errors, as in the payload.
func decodeCSVJSON(v string, out any) error {
	d := json.NewDecoder(strings.NewReader(v))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

// budgetCSVColumn names the CSV column of a payload field, so a row's error points at
// the cell to fix; empty when it is about the whole row.
func budgetCSVColumn(field string, req budgetRequest) string {
	switch {
	case field == "name", field == "orgId", field == "projectId", field == "displayName", field == "description":
		return field
	case field == "metadata.annotations":
		return csvProjectID
	case strings.HasPrefix(field, "limit."):
		return strings.TrimPrefix(field, "limit.")
	case field == "resource.allocation":
		return csvProviderAllocation
	case strings.HasPrefix(field, "resource"):
		return csvProvider
	case strings.HasPrefix(field, "exemptions"):
		return csvExemptions
	case strings.HasPrefix(field, "warnings.channels"):
		return csvChannels
	case strings.HasPrefix(field, "warnings"):
		return csvThresholds
	}
	if m := budgetFieldIndex.FindStringSubmatch(field); m != nil {
		i, _ := strconv.Atoi(m[1])
		if i < len(req.Consumers) {
			typ := req.Consumers[i].Type
			if typ == budgetTypeAttribute {
				return csvAttributes
			}
			for _, c := range budgetConsumerColumns {
				if c.typ == typ {
					if strings.HasSuffix(field, ".allocation") {
						return c.allocation
					}
					return c.value
				}
			}
		}
	}
	return ""
}

// resolveExisting decides each valid row whose name is already taken (Interfaces 2.5): a
// name held by another kind always fails; an existing budget fails the row (fail, the
// default), is left as it is (skip), or has its editable fields replaced (update).
func (s *budgetService) resolveExisting(ctx context.Context, ouID string, rows []*importRow, onExisting string) error {
	names := []string{}
	for _, row := range rows {
		if row.budget != nil {
			names = append(names, row.name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	taken, err := s.budgetRepo.TakenNames(ctx, ouID, names)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.budget == nil || !containsString(taken, row.name) {
			continue
		}
		incoming := row.budget
		row.budget, row.action = nil, ""
		existing, err := s.budgetRepo.Get(ctx, ouID, row.name)
		if errors.Is(err, utils.ErrBudgetNotFound) {
			row.fail(csvName, utils.ErrCodeBudgetAlreadyExists,
				fmt.Sprintf("%s is already the name of another resource in the organization; choose another name.", row.name))
			continue
		}
		if err != nil {
			return err
		}
		switch onExisting {
		case OnExistingSkip:
			row.action = importSkip
		case OnExistingUpdate:
			if column, message := importUpdateRefusal(existing, incoming); message != "" {
				row.fail(column, importUpdateCode(existing), message)
				continue
			}
			updated := *existing
			updated.Configuration = updatedConfig(existing.Configuration, incoming.Configuration)
			row.budget, row.action = &updated, importUpdate
		default:
			row.fail(csvName, utils.ErrCodeBudgetAlreadyExists, fmt.Sprintf(
				"Budget %s already exists. Import with onExisting=update to update it, or onExisting=skip to leave it as it is.", row.name))
		}
	}
	return nil
}

// importUpdateRefusal returns why a row cannot update an existing budget, and the column
// to fix: a gateway's budget is changed only on the gateway, and what a budget covers is
// fixed after creation (BR-15).
func importUpdateRefusal(existing, incoming *models.Budget) (column, message string) {
	if existing.Origin != models.BudgetOriginControlPlane {
		return csvName, fmt.Sprintf("Budget %s was created on a gateway; change it there.", existing.Name)
	}
	fixed := func(what string) string {
		return fmt.Sprintf("Budget %s already exists with a different %s, which cannot change; create a new budget instead.", existing.Name, what)
	}
	was, now := existing.Configuration, incoming.Configuration
	switch {
	case !sameProject(existing.ProjectID, incoming.ProjectID):
		return csvProjectID, fixed("owning project")
	case was.Limit.Period != now.Limit.Period:
		return csvPeriod, fixed("period")
	case was.Limit.Currency != now.Limit.Currency:
		return csvCurrency, fixed("currency")
	case !reflect.DeepEqual(was.Resource, now.Resource):
		return csvProvider, fixed("provider")
	case !reflect.DeepEqual(was.Consumers, now.Consumers):
		return "", fixed("set of consumers")
	}
	return "", ""
}

func importUpdateCode(existing *models.Budget) string {
	if existing.Origin != models.BudgetOriginControlPlane {
		return utils.ErrCodeBudgetReadOnly
	}
	return utils.ErrCodeValidation
}

func sameProject(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// updatedConfig is a stored budget with a row's editable fields, as PUT replaces them; its
// status stays.
func updatedConfig(stored, incoming models.BudgetConfig) models.BudgetConfig {
	out := stored
	out.DisplayName, out.Description = incoming.DisplayName, incoming.Description
	out.Limit.Amount, out.Limit.Action, out.Limit.BlockAt = incoming.Limit.Amount, incoming.Limit.Action, incoming.Limit.BlockAt
	out.Exemptions, out.Warnings = incoming.Exemptions, incoming.Warnings
	return out
}

// importResult reports every row, in file order, with its errors or what it does.
func importResult(rows []*importRow, dryRun bool) *spec.BudgetImportResult {
	result := &spec.BudgetImportResult{DryRun: dryRun, Results: make([]spec.BudgetImportRowResult, 0, len(rows))}
	for _, row := range rows {
		r := spec.BudgetImportRowResult{Row: row.row}
		if row.name != "" {
			name := row.name
			r.Name = &name
		}
		if len(row.errs) > 0 {
			r.Errors = row.errs
			result.Summary.Error++
		} else {
			result.Summary.Valid++
			action := row.action
			r.Action = &action
			switch action {
			case importCreate:
				result.Summary.Created++
			case importUpdate:
				result.Summary.Updated++
			case importSkip:
				result.Summary.Skipped++
			}
		}
		result.Results = append(result.Results, r)
	}
	return result
}
