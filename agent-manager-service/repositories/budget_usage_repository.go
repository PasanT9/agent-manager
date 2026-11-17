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
		for _, u := range batch {
			stored, changed, err := mergeUsageRow(tx, u)
			if err != nil {
				return err
			}
			if !changed || warn == nil {
				continue
			}
			more, err := queueDeliveries(tx, warn(stored))
			if err != nil {
				return err
			}
			queued = append(queued, more...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return queued, nil
}

// mergeUsageRow merges one report into its period's row under the row's lock, and
// returns the stored row and whether it changed.
func mergeUsageRow(tx *gorm.DB, u models.BudgetUsage) (models.BudgetUsage, bool, error) {
	// A report updates only its own period's row; earlier periods are kept.
	const where = "budget_id = ? AND allowance_id = ? AND period_start = ?"
	args := []any{u.BudgetID, u.AllowanceID, u.PeriodStart}
	locked, err := dbx.LockRows(tx, "budget_usage", "spent", where, args...)
	if err != nil {
		return u, false, err
	}
	if locked == 0 {
		stored := u
		inserted, err := dbx.InsertIfAbsent(tx, &stored)
		if err != nil {
			return u, false, err
		}
		if inserted {
			return stored, true, nil
		}
		// Another report inserted it first: merge under its lock.
		if err := dbx.LockRow(tx, "budget_usage", "spent", where, args...); err != nil {
			return u, false, err
		}
	}
	var current models.BudgetUsage
	if err := tx.Where(where, args...).Take(&current).Error; err != nil {
		return u, false, err
	}
	merged, changed := mergeUsage(current, u)
	if !changed {
		return current, false, nil
	}
	if err := tx.Model(&models.BudgetUsage{}).Where(where, args...).Updates(map[string]any{
		"allowance":         dbx.JSON(merged.Allowance),
		"spent":             merged.Spent,
		"unpriced_requests": merged.UnpricedRequests,
		"updated_at":        merged.UpdatedAt,
	}).Error; err != nil {
		return u, false, err
	}
	return merged, true, nil
}

// mergeUsage merges a report into the stored row and reports whether anything changed.
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
