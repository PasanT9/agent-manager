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
	"regexp"
	"strconv"
	"strings"

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

// importRow is one CSV row on its way to becoming a budget.
type importRow struct {
	row    int32
	name   string
	req    spec.BudgetRequest
	budget *models.Budget
	errs   []spec.BudgetImportRowError
}

func (r *importRow) fail(column, code, message string) {
	e := spec.BudgetImportRowError{Code: code, Message: message}
	if column != "" {
		e.Column = &column
	}
	r.errs = append(r.errs, e)
}

func (s *budgetService) Import(ctx context.Context, ouID, createdBy string, file io.Reader, dryRun bool) (*spec.BudgetImportResult, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
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
	channels, err := s.channelIDsByName(ctx, ouID)
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
		row.req = budgetRequestFromCSV(cells, orgName, channels, row)
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
			row.budget = budget
		}
	}
	if err := s.markTakenNames(ctx, ouID, rows); err != nil {
		return nil, err
	}

	result := importResult(rows, dryRun)
	if dryRun || result.Summary.Error > 0 {
		return result, nil
	}
	budgets := make([]*models.Budget, len(rows))
	names := make([]string, len(rows))
	for i, row := range rows {
		budgets[i], names[i] = row.budget, row.name
	}
	if err := s.budgetRepo.CreateManyForControlPlane(ctx, budgets, names); err != nil {
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
	for _, b := range budgets {
		s.gateways.SendBudget(ctx, b, true)
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
// read (a number, JSON, an unknown channel) fail their column on row.
func budgetRequestFromCSV(cells map[string]string, orgName string, channels map[string]string, row *importRow) spec.BudgetRequest {
	optional := func(column string) *string {
		if v := cells[column]; v != "" {
			return &v
		}
		return nil
	}
	req := spec.BudgetRequest{
		Name: cells[csvName], OrgId: orgName, ProjectId: optional(csvProjectID),
		DisplayName: optional(csvDisplayName), Description: optional(csvDescription),
		Limit: spec.BudgetLimit{
			Amount: cells[csvAmount], Currency: optional(csvCurrency), Period: cells[csvPeriod], Action: cells[csvAction],
		},
	}
	if v := cells[csvOrgID]; v != "" {
		req.OrgId = v
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
		warnings := &spec.BudgetWarnings{Thresholds: []int32{}, ChannelIds: []string{}}
		for _, v := range csvList(cells[csvThresholds]) {
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				row.fail(csvThresholds, utils.ErrCodeValidation, "thresholds must be whole percentages separated by ';', e.g. 80;100.")
				break
			}
			warnings.Thresholds = append(warnings.Thresholds, int32(n))
		}
		for _, name := range csvList(cells[csvChannels]) {
			id, ok := channels[name]
			if !ok {
				row.fail(csvChannels, utils.ErrCodeChannelNotAvailable, fmt.Sprintf("Notification channel %s does not exist.", name))
				continue
			}
			warnings.ChannelIds = append(warnings.ChannelIds, id)
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
func budgetCSVColumn(field string, req spec.BudgetRequest) string {
	switch {
	case field == "name", field == "orgId", field == "projectId", field == "displayName", field == "description":
		return field
	case strings.HasPrefix(field, "limit."):
		return strings.TrimPrefix(field, "limit.")
	case field == "resource.allocation":
		return csvProviderAllocation
	case strings.HasPrefix(field, "resource"):
		return csvProvider
	case strings.HasPrefix(field, "exemptions"):
		return csvExemptions
	case strings.HasPrefix(field, "warnings.channelIds"):
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

// channelIDsByName maps the organization's notification channel names, unique in it, to
// their IDs: the CSV names channels, the payload takes IDs.
func (s *budgetService) channelIDsByName(ctx context.Context, ouID string) (map[string]string, error) {
	destinations, err := s.eventRepo.ListDestinations(ctx, ouID)
	if err != nil {
		return nil, fmt.Errorf("failed to list notification channels: %w", err)
	}
	out := make(map[string]string, len(destinations))
	for _, d := range destinations {
		out[d.Name] = d.ID.String()
	}
	return out, nil
}

// markTakenNames fails the rows whose name another artifact of the organization holds.
func (s *budgetService) markTakenNames(ctx context.Context, ouID string, rows []*importRow) error {
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
		if row.budget != nil && containsString(taken, row.name) {
			row.budget = nil
			row.fail(csvName, utils.ErrCodeBudgetAlreadyExists, budgetNameTaken(row.name).Error())
		}
	}
	return nil
}

// importResult reports every row, in file order, with its errors.
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
		}
		result.Results = append(result.Results, r)
	}
	return result
}
