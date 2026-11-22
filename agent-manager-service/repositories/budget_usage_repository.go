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
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db/dbx"
	"github.com/wso2/agent-manager/agent-manager-service/models"
)

// BudgetUsageRepository keeps each budget allowance's spend in its current period
// (Interfaces 1.2) and queues the warnings it crosses (Interfaces 6).
//
//go:generate moq -rm -fmt goimports -skip-ensure -pkg repomocks -out repomocks/budget_usage_repository_mock.go . BudgetUsageRepository:BudgetUsageRepositoryMock
type BudgetUsageRepository interface {
	// Record merges a gateway's reports into their allowances' rows, all in one
	// transaction: each period has its own row, and the same period keeps the higher
	// totals (each gateway reports the shared total; one may lag). warn gets each row that
	// changed and returns the warning deliveries it calls for; Record queues them in the
	// same transaction and returns those not queued before.
	Record(ctx context.Context, rows []models.BudgetUsage,
		warn func(stored models.BudgetUsage) []models.EventDelivery) ([]models.EventDelivery, error)
	// ForBudget returns the budget's allowances.
	ForBudget(ctx context.Context, budgetID uuid.UUID) ([]models.BudgetUsage, error)
	// ForBudgets returns the allowances of the organization's budgets with those IDs.
	ForBudgets(ctx context.Context, ouID string, budgetIDs []uuid.UUID) ([]models.BudgetUsage, error)
	// QueueWarnings queues warning deliveries and returns those not queued before.
	QueueWarnings(ctx context.Context, deliveries []models.EventDelivery) ([]models.EventDelivery, error)
}

type budgetUsageRepository struct {
	db *gorm.DB
}

// NewBudgetUsageRepository creates a BudgetUsageRepository.
func NewBudgetUsageRepository(db *gorm.DB) BudgetUsageRepository {
	return &budgetUsageRepository{db: db}
}

func (r *budgetUsageRepository) Record(ctx context.Context, rows []models.BudgetUsage,
	warn func(stored models.BudgetUsage) []models.EventDelivery,
) ([]models.EventDelivery, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	now := dbx.Now()
	batch := make([]models.BudgetUsage, len(rows))
	for i, u := range rows {
		u.PeriodStart = u.PeriodStart.UTC()
		u.UpdatedAt = now
		batch[i] = u
	}
	// Rows are locked in key order, so two batches never wait on each other's rows.
	sort.Slice(batch, func(i, j int) bool {
		a, b := batch[i], batch[j]
		if a.BudgetID != b.BudgetID {
			return a.BudgetID.String() < b.BudgetID.String()
		}
		if a.AllowanceID != b.AllowanceID {
			return a.AllowanceID.String() < b.AllowanceID.String()
		}
		return a.PeriodStart.Before(b.PeriodStart)
	})
	var queued []models.EventDelivery
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		changed, err := writeUsageBatch(tx, batch)
		if err != nil {
			return err
		}
		if warn == nil {
			return nil
		}
		for _, stored := range changed {
			more, err := queueDeliveries(tx, warn(stored))
			if err != nil {
				return err
			}
			queued = append(queued, more...)
		}
		return nil
	})
	return queued, err
}

// usageBatchSize bounds one multi-row insert (8 columns; SQL Server allows 2,100 parameters).
const usageBatchSize = 200

type usageRowKey struct {
	budget, allowance uuid.UUID
	period            int64
}

func usageRowKeyOf(u models.BudgetUsage) usageRowKey {
	return usageRowKey{u.BudgetID, u.AllowanceID, u.PeriodStart.UTC().UnixMicro()}
}

// writeUsageBatch writes a batch of reported totals with one read and, per row that is
// new or higher, one write: new rows in multi-row inserts, higher ones by a conditional
// update on the value just read, with no row lock. A row another writer changed in
// between is read again and merged; a row another writer inserted first is merged into.
// It returns every row as stored after a change, for the warnings.
func writeUsageBatch(tx *gorm.DB, batch []models.BudgetUsage) ([]models.BudgetUsage, error) {
	stored, err := storedUsage(tx, batch)
	if err != nil {
		return nil, err
	}
	var inserts []models.BudgetUsage
	type update struct{ current, merged models.BudgetUsage }
	var updates []update
	for _, u := range batch {
		current, ok := stored[usageRowKeyOf(u)]
		if !ok {
			inserts = append(inserts, u)
			continue
		}
		if merged, changed := mergeUsage(current, u); changed {
			updates = append(updates, update{current, merged})
		}
	}
	var changed []models.BudgetUsage
	for start := 0; start < len(inserts); start += usageBatchSize {
		chunk := inserts[start:min(start+usageBatchSize, len(inserts))]
		inserted, err := dbx.InsertIfAbsent(tx, &chunk)
		if err != nil {
			return nil, err
		}
		if inserted {
			changed = append(changed, chunk...)
			continue
		}
		// Another writer inserted one of them first: row by row, merging into those.
		for _, u := range chunk {
			row := u
			inserted, err := dbx.InsertIfAbsent(tx, &row)
			if err != nil {
				return nil, err
			}
			if inserted {
				changed = append(changed, row)
				continue
			}
			merged, ok, err := mergeUsageRetrying(tx, u)
			if err != nil {
				return nil, err
			}
			if ok {
				changed = append(changed, merged)
			}
		}
	}
	for _, up := range updates {
		ok, err := updateUsageIf(tx, up.current, up.merged)
		if err != nil {
			return nil, err
		}
		if ok {
			changed = append(changed, up.merged)
			continue
		}
		merged, ok, err := mergeUsageRetrying(tx, up.merged)
		if err != nil {
			return nil, err
		}
		if ok {
			changed = append(changed, merged)
		}
	}
	return changed, nil
}

// storedUsage reads the stored rows of the batch's allowances in one query per chunk.
func storedUsage(tx *gorm.DB, batch []models.BudgetUsage) (map[usageRowKey]models.BudgetUsage, error) {
	budgetSet := map[uuid.UUID]bool{}
	periodSet := map[int64]time.Time{}
	for _, u := range batch {
		budgetSet[u.BudgetID] = true
		periodSet[u.PeriodStart.UTC().UnixMicro()] = u.PeriodStart.UTC()
	}
	budgets := make([]uuid.UUID, 0, len(budgetSet))
	for id := range budgetSet {
		budgets = append(budgets, id)
	}
	periods := make([]time.Time, 0, len(periodSet))
	for _, t := range periodSet {
		periods = append(periods, t)
	}
	out := make(map[usageRowKey]models.BudgetUsage, len(batch))
	for _, chunk := range dbx.Chunks(budgets) {
		var rows []models.BudgetUsage
		if err := tx.Where("budget_id IN ? AND period_start IN ?", chunk, periods).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("failed to read budget usage: %w", err)
		}
		for _, row := range rows {
			out[usageRowKeyOf(row)] = row
		}
	}
	return out, nil
}

// updateUsageIf raises a row to merged only if it still holds current; false when another
// writer changed it in between.
func updateUsageIf(tx *gorm.DB, current, merged models.BudgetUsage) (bool, error) {
	res := tx.Model(&models.BudgetUsage{}).
		Where("budget_id = ? AND allowance_id = ? AND period_start = ? AND spent = ? AND unpriced_requests = ?",
			current.BudgetID, current.AllowanceID, current.PeriodStart, current.Spent, current.UnpricedRequests).
		Updates(map[string]any{
			"allowance":         dbx.JSON(merged.Allowance),
			"spent":             merged.Spent,
			"unpriced_requests": merged.UnpricedRequests,
			"updated_at":        merged.UpdatedAt,
		})
	return res.RowsAffected == 1, res.Error
}

// mergeUsageRetrying reads a row another writer just changed and merges the report into
// it, a few times; false when the stored row is already as high.
func mergeUsageRetrying(tx *gorm.DB, report models.BudgetUsage) (models.BudgetUsage, bool, error) {
	for range 5 {
		var current models.BudgetUsage
		err := tx.Where("budget_id = ? AND allowance_id = ? AND period_start = ?",
			report.BudgetID, report.AllowanceID, report.PeriodStart).Take(&current).Error
		if err != nil {
			return report, false, err
		}
		merged, changed := mergeUsage(current, report)
		if !changed {
			return current, false, nil
		}
		ok, err := updateUsageIf(tx, current, merged)
		if err != nil || ok {
			return merged, ok, err
		}
	}
	return report, false, fmt.Errorf("budget usage %s/%s kept changing while it was written", report.BudgetID, report.AllowanceID)
}

func mergeUsage(current, report models.BudgetUsage) (models.BudgetUsage, bool) {
	switch {
	case report.PeriodStart.Before(current.PeriodStart):
		return current, false
	case report.PeriodStart.After(current.PeriodStart):
		return report, true
	}
	merged, changed := current, false
	if greater(report.Spent, current.Spent) {
		merged.Spent, changed = report.Spent, true
	}
	if report.UnpricedRequests > current.UnpricedRequests {
		merged.UnpricedRequests, changed = report.UnpricedRequests, true
	}
	if changed {
		merged.UpdatedAt = report.UpdatedAt
		if len(report.Allowance) > 0 {
			merged.Allowance = report.Allowance
		}
	}
	return merged, changed
}

// greater reports whether decimal a is greater than decimal b; an unreadable value is 0.
func greater(a, b string) bool {
	x, ok := new(big.Rat).SetString(a)
	if !ok {
		x = new(big.Rat)
	}
	y, ok := new(big.Rat).SetString(b)
	if !ok {
		y = new(big.Rat)
	}
	return x.Cmp(y) > 0
}

// ForBudget returns each of a budget's allowances in its latest period.
func (r *budgetUsageRepository) ForBudget(ctx context.Context, budgetID uuid.UUID) ([]models.BudgetUsage, error) {
	var rows []models.BudgetUsage
	err := r.db.WithContext(ctx).Where("budget_id = ?", budgetID).Order("allowance_id").Find(&rows).Error
	return latestPeriods(rows), err
}

// latestPeriods keeps each allowance's latest period, in the order the rows came.
func latestPeriods(rows []models.BudgetUsage) []models.BudgetUsage {
	type key struct{ budget, allowance uuid.UUID }
	latest := map[key]int{}
	out := make([]models.BudgetUsage, 0, len(rows))
	for _, row := range rows {
		k := key{row.BudgetID, row.AllowanceID}
		if i, seen := latest[k]; seen {
			if row.PeriodStart.After(out[i].PeriodStart) {
				out[i] = row
			}
			continue
		}
		latest[k] = len(out)
		out = append(out, row)
	}
	return out
}

func (r *budgetUsageRepository) ForBudgets(ctx context.Context, ouID string, budgetIDs []uuid.UUID) ([]models.BudgetUsage, error) {
	rows := []models.BudgetUsage{}
	for _, chunk := range dbx.Chunks(budgetIDs) {
		var page []models.BudgetUsage
		if err := r.db.WithContext(ctx).Where("org_id = ? AND budget_id IN ?", ouID, chunk).Find(&page).Error; err != nil {
			return nil, fmt.Errorf("failed to read budget usage: %w", err)
		}
		rows = append(rows, page...)
	}
	return latestPeriods(rows), nil
}

func (r *budgetUsageRepository) QueueWarnings(ctx context.Context, deliveries []models.EventDelivery) ([]models.EventDelivery, error) {
	var queued []models.EventDelivery
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		queued, err = queueDeliveries(tx, deliveries)
		return err
	})
	return queued, err
}

// queueDeliveries records pending deliveries and returns those inserted: a warning is
// queued once per destination (its id is unique per destination).
func queueDeliveries(tx *gorm.DB, deliveries []models.EventDelivery) ([]models.EventDelivery, error) {
	var queued []models.EventDelivery
	now := dbx.Now()
	for i := range deliveries {
		d := deliveries[i]
		d.CreatedAt, d.UpdatedAt = now, now
		if d.Status == "" {
			d.Status = models.EventDeliveryPending
		}
		inserted, err := insertDelivery(tx, &d)
		if err != nil {
			return nil, err
		}
		if inserted {
			queued = append(queued, d)
		}
	}
	return queued, nil
}
