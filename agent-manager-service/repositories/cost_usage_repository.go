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
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db/dbx"
	"github.com/wso2/agent-manager/agent-manager-service/models"
)

// CostUsageRepository keeps what LLM traffic cost per month and combination of values
// (AI cost stats, Interfaces 1.3).
//
//go:generate moq -rm -fmt goimports -skip-ensure -pkg repomocks -out repomocks/cost_usage_repository_mock.go . CostUsageRepository:CostUsageRepositoryMock
type CostUsageRepository interface {
	// Record merges a gateway's reports into their rows, all in one transaction: each
	// total keeps the higher value (every gateway reports the shared total; one may lag).
	Record(ctx context.Context, rows []models.CostUsage) error
	// Since returns the organization's rows from the month starting at from.
	Since(ctx context.Context, ouID string, from time.Time) ([]models.CostUsage, error)
	// DeleteBefore deletes every organization's rows of months before before, and
	// returns how many went.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

type costUsageRepository struct {
	db *gorm.DB
}

// NewCostUsageRepository creates a CostUsageRepository.
func NewCostUsageRepository(db *gorm.DB) CostUsageRepository {
	return &costUsageRepository{db: db}
}

func (r *costUsageRepository) Record(ctx context.Context, rows []models.CostUsage) error {
	if len(rows) == 0 {
		return nil
	}
	now := dbx.Now()
	batch := make([]models.CostUsage, len(rows))
	for i, u := range rows {
		u.Month = u.Month.UTC()
		u.UpdatedAt = now
		batch[i] = u
	}
	// Rows are locked in key order, so two batches never wait on each other's rows.
	sort.Slice(batch, func(i, j int) bool {
		a, b := batch[i], batch[j]
		if a.OUID != b.OUID {
			return a.OUID < b.OUID
		}
		if !a.Month.Equal(b.Month) {
			return a.Month.Before(b.Month)
		}
		return a.CostKey.String() < b.CostKey.String()
	})
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, u := range batch {
			if err := mergeCostRow(tx, u); err != nil {
				return err
			}
		}
		return nil
	})
}

// mergeCostRow merges one report into its row under the row's lock.
func mergeCostRow(tx *gorm.DB, u models.CostUsage) error {
	const where = "org_id = ? AND month = ? AND cost_key = ?"
	args := []any{u.OUID, u.Month, u.CostKey}
	locked, err := dbx.LockRows(tx, "cost_usage", "cost", where, args...)
	if err != nil {
		return err
	}
	if locked == 0 {
		stored := u
		inserted, err := dbx.InsertIfAbsent(tx, &stored)
		if err != nil || inserted {
			return err
		}
		// Another report inserted it first: merge under its lock.
		if err := dbx.LockRow(tx, "cost_usage", "cost", where, args...); err != nil {
			return err
		}
	}
	var current models.CostUsage
	if err := tx.Where(where, args...).Take(&current).Error; err != nil {
		return err
	}
	updates := map[string]any{}
	if greater(u.Cost, current.Cost) {
		updates["cost"] = u.Cost
	}
	if u.MissingPriceRequests > current.MissingPriceRequests {
		updates["missing_price_requests"] = u.MissingPriceRequests
	}
	if u.UsageUnavailableRequests > current.UsageUnavailableRequests {
		updates["usage_unavailable_requests"] = u.UsageUnavailableRequests
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = u.UpdatedAt
	return tx.Model(&models.CostUsage{}).Where(where, args...).Updates(updates).Error
}

func (r *costUsageRepository) Since(ctx context.Context, ouID string, from time.Time) ([]models.CostUsage, error) {
	rows := []models.CostUsage{}
	if err := r.db.WithContext(ctx).Where("org_id = ? AND month >= ?", ouID, from.UTC()).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to read cost usage: %w", err)
	}
	return rows, nil
}

func (r *costUsageRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Where("month < ?", before.UTC()).Delete(&models.CostUsage{})
	if res.Error != nil {
		return 0, fmt.Errorf("failed to delete old cost usage: %w", res.Error)
	}
	return res.RowsAffected, nil
}
