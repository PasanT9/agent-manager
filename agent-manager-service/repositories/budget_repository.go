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
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db/dbx"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// BudgetRepository stores budgets, whoever created them: the control plane, or a
// gateway through its own API. A budget is an artifact of kind Budget plus a budgets
// row, as an LLM provider is; deleting the artifact deletes the row.
//
//go:generate moq -rm -fmt goimports -skip-ensure -pkg repomocks -out repomocks/budget_repository_mock.go . BudgetRepository:BudgetRepositoryMock
type BudgetRepository interface {
	// CreateForControlPlane creates a control-plane budget named name: its artifact and
	// row. budget.ID is set when nil, and Name and the timestamps are filled in. It returns
	// utils.ErrBudgetAlreadyExists when another artifact holds the name.
	CreateForControlPlane(ctx context.Context, budget *models.Budget, name string) error
	// SaveForGateway stores a budget a gateway pushed, matched by name (the artifact
	// handle): it updates the gateway's earlier copy, keeping its ID, or creates the
	// artifact and the row. budget.ID is set to the stored budget's ID. It reports false,
	// storing nothing, when the name is taken by another artifact (a control-plane
	// budget, another gateway's, or another kind).
	SaveForGateway(ctx context.Context, budget *models.Budget, name string) (bool, error)
	// DeleteForGateway deletes the gateway's budgets with the given names.
	DeleteForGateway(ctx context.Context, ouID string, gatewayID uuid.UUID, names []string) error
	// DeleteForGatewayExcept deletes the gateway's budgets whose name is not in keep.
	DeleteForGatewayExcept(ctx context.Context, ouID string, gatewayID uuid.UUID, keep []string) error
	// Get returns the organization's budget named name, or utils.ErrBudgetNotFound.
	Get(ctx context.Context, ouID, name string) (*models.Budget, error)
	// Update replaces a budget's configuration and touches its artifact's updated_at.
	Update(ctx context.Context, budget *models.Budget) error
	// Delete deletes the organization's budget named name, artifact and row, or returns
	// utils.ErrBudgetNotFound.
	Delete(ctx context.Context, ouID, name string) error
	// List returns the organization's budgets of every origin, with their names, ordered by name.
	List(ctx context.Context, ouID string) ([]models.Budget, error)
}

type budgetRepository struct {
	db *gorm.DB
}

// NewBudgetRepository creates a BudgetRepository.
func NewBudgetRepository(db *gorm.DB) BudgetRepository {
	return &budgetRepository{db: db}
}

func (r *budgetRepository) CreateForControlPlane(ctx context.Context, budget *models.Budget, name string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if budget.ID == uuid.Nil {
			budget.ID = uuid.Must(uuid.NewV7())
		}
		now := dbx.Now()
		inserted, err := dbx.InsertIfAbsent(tx, &models.Artifact{
			UUID: budget.ID, Handle: name, Name: name, Version: models.BudgetVersion,
			Kind: models.KindBudget, OUID: budget.OrgID, InCatalog: false,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("failed to create budget artifact %q: %w", name, err)
		}
		if !inserted {
			return utils.ErrBudgetAlreadyExists
		}
		if err := tx.Create(budget).Error; err != nil {
			return fmt.Errorf("failed to create budget %q: %w", name, err)
		}
		budget.Name, budget.CreatedAt, budget.UpdatedAt = name, now, now
		return nil
	})
}

func (r *budgetRepository) SaveForGateway(ctx context.Context, budget *models.Budget, name string) (bool, error) {
	stored := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stored = false
		if budget.ID == uuid.Nil {
			budget.ID = uuid.Must(uuid.NewV7())
		}
		now := dbx.Now()
		artifact := &models.Artifact{
			UUID: budget.ID, Handle: name, Name: name, Version: models.BudgetVersion,
			Kind: models.KindBudget, OUID: budget.OrgID, InCatalog: false,
			CreatedAt: now, UpdatedAt: now,
		}
		inserted, err := dbx.InsertIfAbsent(tx, artifact)
		if err != nil {
			return fmt.Errorf("failed to create budget artifact %q: %w", name, err)
		}
		if inserted {
			if err := tx.Create(budget).Error; err != nil {
				return fmt.Errorf("failed to create budget %q: %w", name, err)
			}
			stored = true
			return nil
		}

		// The name is taken: update it only if it is this gateway's budget.
		var existing models.Budget
		err = tx.Table("budgets").Select("budgets.*").
			Joins("JOIN artifacts ON artifacts.id = budgets.id AND artifacts.org_id = budgets.org_id").
			Where("artifacts.org_id = ? AND artifacts.handle = ? AND artifacts.kind = ?", budget.OrgID, name, models.KindBudget).
			Where("budgets.origin = ? AND budgets.gateway_id = ?", models.BudgetOriginGateway, budget.GatewayID).
			Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read budget %q: %w", name, err)
		}
		if err := tx.Model(&models.Budget{}).Where("id = ? AND org_id = ?", existing.ID, budget.OrgID).
			Update("configuration", dbx.JSON(budget.Configuration)).Error; err != nil {
			return fmt.Errorf("failed to update budget %q: %w", name, err)
		}
		if err := NewArtifactRepo(tx).Update(tx, &models.Artifact{UUID: existing.ID, OUID: budget.OrgID}); err != nil {
			return fmt.Errorf("failed to update budget artifact %q: %w", name, err)
		}
		budget.ID, stored = existing.ID, true
		return nil
	})
	return stored, err
}

// gatewayBudgetIDs returns the IDs of the gateway's budgets, limited to names when given.
func gatewayBudgetIDs(tx *gorm.DB, ouID string, gatewayID uuid.UUID, names []string) ([]uuid.UUID, error) {
	q := tx.Table("budgets").
		Joins("JOIN artifacts ON artifacts.id = budgets.id AND artifacts.org_id = budgets.org_id").
		Where("budgets.org_id = ? AND budgets.origin = ? AND budgets.gateway_id = ?", ouID, models.BudgetOriginGateway, gatewayID)
	if names != nil {
		q = q.Where("artifacts.handle IN ?", names)
	}
	var ids []uuid.UUID
	if err := q.Pluck("budgets.id", &ids).Error; err != nil {
		return nil, fmt.Errorf("failed to list gateway budgets: %w", err)
	}
	return ids, nil
}

// deleteBudgetArtifacts deletes budgets by deleting their artifacts.
func deleteBudgetArtifacts(tx *gorm.DB, ouID string, ids []uuid.UUID) error {
	for _, chunk := range dbx.Chunks(ids) {
		if err := tx.Where("org_id = ? AND kind = ? AND id IN ?", ouID, models.KindBudget, chunk).
			Delete(&models.Artifact{}).Error; err != nil {
			return fmt.Errorf("failed to delete budgets: %w", err)
		}
	}
	return nil
}

func (r *budgetRepository) DeleteForGateway(ctx context.Context, ouID string, gatewayID uuid.UUID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, chunk := range dbx.Chunks(names) {
			ids, err := gatewayBudgetIDs(tx, ouID, gatewayID, chunk)
			if err != nil {
				return err
			}
			if err := deleteBudgetArtifacts(tx, ouID, ids); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *budgetRepository) DeleteForGatewayExcept(ctx context.Context, ouID string, gatewayID uuid.UUID, keep []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Found here and deleted by ID, since a NOT IN list cannot be split into the
		// database's IN-list limit.
		var current []struct {
			ID     uuid.UUID
			Handle string
		}
		if err := tx.Table("budgets").Select("budgets.id AS id, artifacts.handle AS handle").
			Joins("JOIN artifacts ON artifacts.id = budgets.id AND artifacts.org_id = budgets.org_id").
			Where("budgets.org_id = ? AND budgets.origin = ? AND budgets.gateway_id = ?", ouID, models.BudgetOriginGateway, gatewayID).
			Scan(&current).Error; err != nil {
			return fmt.Errorf("failed to list gateway budgets: %w", err)
		}
		kept := make(map[string]bool, len(keep))
		for _, name := range keep {
			kept[name] = true
		}
		stale := make([]uuid.UUID, 0, len(current))
		for _, c := range current {
			if !kept[c.Handle] {
				stale = append(stale, c.ID)
			}
		}
		return deleteBudgetArtifacts(tx, ouID, stale)
	})
}

// DeleteGatewayBudgets deletes a gateway's budgets in the caller's transaction, before
// the gateway itself: budgets.gateway_id takes no action (schema rule 7).
func DeleteGatewayBudgets(tx *gorm.DB, ouID, gatewayID string) error {
	ids := tx.Model(&models.Budget{}).Select("id").Where("org_id = ? AND gateway_id = ?", ouID, gatewayID)
	return tx.Where("org_id = ? AND kind = ? AND id IN (?)", ouID, models.KindBudget, ids).
		Delete(&models.Artifact{}).Error
}

// budgetsWithArtifact selects budgets with their artifact's handle and timestamps.
func budgetsWithArtifact(db *gorm.DB) *gorm.DB {
	return db.Table("budgets").Select("budgets.*, artifacts.handle AS name, artifacts.created_at, artifacts.updated_at").
		Joins("JOIN artifacts ON artifacts.id = budgets.id AND artifacts.org_id = budgets.org_id")
}

func (r *budgetRepository) Get(ctx context.Context, ouID, name string) (*models.Budget, error) {
	var b models.Budget
	err := budgetsWithArtifact(r.db.WithContext(ctx)).
		Where("budgets.org_id = ? AND artifacts.kind = ? AND artifacts.handle = ?", ouID, models.KindBudget, name).
		Take(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, utils.ErrBudgetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get budget %q: %w", name, err)
	}
	return &b, nil
}

func (r *budgetRepository) Update(ctx context.Context, budget *models.Budget) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Budget{}).Where("id = ? AND org_id = ?", budget.ID, budget.OrgID).
			Update("configuration", dbx.JSON(budget.Configuration))
		if res.Error != nil {
			return fmt.Errorf("failed to update budget %q: %w", budget.Name, res.Error)
		}
		if res.RowsAffected == 0 {
			return utils.ErrBudgetNotFound
		}
		artifact := &models.Artifact{UUID: budget.ID, OUID: budget.OrgID}
		if err := NewArtifactRepo(tx).Update(tx, artifact); err != nil {
			return fmt.Errorf("failed to update budget artifact %q: %w", budget.Name, err)
		}
		budget.UpdatedAt = artifact.UpdatedAt
		return nil
	})
}

func (r *budgetRepository) Delete(ctx context.Context, ouID, name string) error {
	res := r.db.WithContext(ctx).Where("org_id = ? AND kind = ? AND handle = ?", ouID, models.KindBudget, name).
		Delete(&models.Artifact{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete budget %q: %w", name, res.Error)
	}
	if res.RowsAffected == 0 {
		return utils.ErrBudgetNotFound
	}
	return nil
}

func (r *budgetRepository) List(ctx context.Context, ouID string) ([]models.Budget, error) {
	rows := []models.Budget{}
	if err := budgetsWithArtifact(r.db.WithContext(ctx)).
		Where("budgets.org_id = ?", ouID).Order("artifacts.handle").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list budgets: %w", err)
	}
	return rows, nil
}
