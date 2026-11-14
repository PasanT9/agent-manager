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

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db/dbx"
	"github.com/wso2/agent-manager/agent-manager-service/models"
)

// BudgetRepository stores budgets, whoever created them: the control plane, or a
// gateway through its own API.
//
//go:generate moq -rm -fmt goimports -skip-ensure -pkg repomocks -out repomocks/budget_repository_mock.go . BudgetRepository:BudgetRepositoryMock
type BudgetRepository interface {
	// SaveForGateway stores a budget a gateway pushed, matched by name: it updates the
	// gateway's earlier copy, keeping its ID, or inserts it with budget.ID. budget.ID is set
	// to the stored row's ID. It reports false, storing nothing, when another budget (the
	// control plane's or another gateway's) holds the name.
	SaveForGateway(ctx context.Context, budget *models.Budget) (bool, error)
	// DeleteForGateway deletes the gateway's budgets with the given names.
	DeleteForGateway(ctx context.Context, ouID string, gatewayID uuid.UUID, names []string) error
	// DeleteForGatewayExcept deletes the gateway's budgets whose name is not in keep.
	DeleteForGatewayExcept(ctx context.Context, ouID string, gatewayID uuid.UUID, keep []string) error
	// List returns the organization's budgets of every origin, ordered by name.
	List(ctx context.Context, ouID string) ([]models.Budget, error)
}

type budgetRepository struct {
	db *gorm.DB
}

// NewBudgetRepository creates a BudgetRepository.
func NewBudgetRepository(db *gorm.DB) BudgetRepository {
	return &budgetRepository{db: db}
}

// gatewayOwned matches the budgets the gateway created.
const gatewayOwned = "org_id = ? AND origin = ? AND gateway_id = ?"

func (r *budgetRepository) SaveForGateway(ctx context.Context, budget *models.Budget) (bool, error) {
	stored := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stored = false
		// Insert, or update this gateway's copy of the name. Another budget holding the name
		// is never touched: the update matches no row.
		owned := func() *gorm.DB {
			return tx.Model(&models.Budget{}).Where(gatewayOwned+" AND name = ?",
				budget.OUID, models.BudgetOriginGateway, budget.GatewayID, budget.Name)
		}
		updated := int64(0)
		inserted, err := dbx.Upsert(tx, budget, func(*gorm.DB) error {
			res := owned().Updates(map[string]any{
				"configuration": dbx.JSON(budget.Configuration),
				"updated_at":    budget.UpdatedAt,
				"synced_at":     budget.SyncedAt,
			})
			updated = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return fmt.Errorf("failed to store budget %q: %w", budget.Name, err)
		}
		if inserted {
			stored = true
			return nil
		}
		if updated == 0 {
			return nil
		}
		var row models.Budget
		if err := owned().Select("id").Take(&row).Error; err != nil {
			return fmt.Errorf("failed to read budget %q: %w", budget.Name, err)
		}
		budget.ID, stored = row.ID, true
		return nil
	})
	return stored, err
}

func (r *budgetRepository) DeleteForGateway(ctx context.Context, ouID string, gatewayID uuid.UUID, names []string) error {
	for _, chunk := range dbx.Chunks(names) {
		if err := r.db.WithContext(ctx).Where(gatewayOwned+" AND name IN ?", ouID, models.BudgetOriginGateway, gatewayID, chunk).
			Delete(&models.Budget{}).Error; err != nil {
			return fmt.Errorf("failed to delete gateway budgets: %w", err)
		}
	}
	return nil
}

func (r *budgetRepository) DeleteForGatewayExcept(ctx context.Context, ouID string, gatewayID uuid.UUID, keep []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Found here and deleted by name, since a NOT IN list cannot be split into the
		// database's IN-list limit.
		var current []string
		if err := tx.Model(&models.Budget{}).Where(gatewayOwned, ouID, models.BudgetOriginGateway, gatewayID).
			Pluck("name", &current).Error; err != nil {
			return fmt.Errorf("failed to list gateway budgets: %w", err)
		}
		kept := make(map[string]bool, len(keep))
		for _, name := range keep {
			kept[name] = true
		}
		stale := make([]string, 0, len(current))
		for _, name := range current {
			if !kept[name] {
				stale = append(stale, name)
			}
		}
		return (&budgetRepository{db: tx}).DeleteForGateway(ctx, ouID, gatewayID, stale)
	})
}

func (r *budgetRepository) List(ctx context.Context, ouID string) ([]models.Budget, error) {
	rows := []models.Budget{}
	if err := r.db.WithContext(ctx).Where("org_id = ?", ouID).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list budgets: %w", err)
	}
	return rows, nil
}
