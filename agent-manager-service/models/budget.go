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

package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BudgetOrigin says who created a budget.
type BudgetOrigin string

const (
	// BudgetOriginControlPlane budgets are created and managed in the control plane.
	BudgetOriginControlPlane BudgetOrigin = "control_plane"
	// BudgetOriginGateway budgets were created by a gateway through its own API and are
	// mirrored from its resource sync; they are read-only in the control plane.
	BudgetOriginGateway BudgetOrigin = "gateway_api"
)

// Budget is a spend limit on LLM traffic. Configuration is the Budget document
// (apiVersion, kind, metadata, spec). Names are unique in the organization.
type Budget struct {
	ID            uuid.UUID       `gorm:"column:id;primaryKey"`
	OUID          string          `gorm:"column:org_id"`
	Name          string          `gorm:"column:name"`
	Origin        BudgetOrigin    `gorm:"column:origin"`
	GatewayID     *uuid.UUID      `gorm:"column:gateway_id"` // set for gateway budgets only
	Configuration json.RawMessage `gorm:"column:configuration;serializer:json"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	UpdatedAt     time.Time       `gorm:"column:updated_at"`
	SyncedAt      *time.Time      `gorm:"column:synced_at"` // last resource sync, gateway budgets only
}

// TableName maps Budget to its table.
func (Budget) TableName() string { return "budgets" }
