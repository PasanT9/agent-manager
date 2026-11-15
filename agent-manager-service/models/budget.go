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
	"time"

	"github.com/google/uuid"
)

// BudgetOrigin says who created a budget.
type BudgetOrigin string

const (
	// BudgetOriginControlPlane budgets are created and managed in the control plane.
	BudgetOriginControlPlane BudgetOrigin = "control_plane"
	// BudgetOriginGateway budgets were created by a gateway through its own API and are
	// pushed by it; they are read-only in the control plane.
	BudgetOriginGateway BudgetOrigin = "gateway_api"
)

// BudgetVersion is the artifact version of every budget: budgets are not versioned.
const BudgetVersion = "v1"

// Budget is a spend limit on LLM traffic, stored as an artifact of kind Budget (its
// handle is the budget name, unique in the organization) plus this row, as an LLM
// provider is. Configuration is the control-plane model, as LLMProviderConfig is for a
// provider: the spec of the Budget kind, which is built from it when it is sent out.
type Budget struct {
	ID            uuid.UUID    `gorm:"column:id;primaryKey"`
	OrgID         string       `gorm:"column:org_id"`
	Origin        BudgetOrigin `gorm:"column:origin"`
	GatewayID     *uuid.UUID   `gorm:"column:gateway_id"` // set for gateway budgets only
	Configuration BudgetConfig `gorm:"column:configuration;serializer:json"`
	CreatedBy     string       `gorm:"column:created_by"`

	// Name and the timestamps come from the artifact, read with a join.
	Name      string    `gorm:"->;column:name"`
	CreatedAt time.Time `gorm:"->;column:created_at"`
	UpdatedAt time.Time `gorm:"->;column:updated_at"`
}

// BudgetConfig is a budget's settings (Interfaces 2.2), with defaults filled in: the
// spec of the Budget kind (Interfaces 3).
type BudgetConfig struct {
	DisplayName  string              `json:"displayName,omitempty"`
	Description  *string             `json:"description,omitempty"`
	Status       string              `json:"status"`
	Limit        BudgetLimit         `json:"limit"`
	Resource     *BudgetResource     `json:"resource,omitempty"`
	Environments *BudgetEnvironments `json:"environments,omitempty"`
	Consumers    []BudgetConsumer    `json:"consumers"`
	Exemptions   []BudgetExemption   `json:"exemptions"`
	Warnings     *BudgetWarnings     `json:"warnings,omitempty"`
}

// BudgetLimit is how much may be spent per period, and what happens when it is used up.
type BudgetLimit struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Action   string `json:"action"`
	BlockAt  *int32 `json:"blockAt,omitempty"`
}

// BudgetResource is the LLM providers a budget covers; nil covers all, shared.
type BudgetResource struct {
	Type       string  `json:"type"`
	Allocation string  `json:"allocation"`
	ID         *string `json:"id,omitempty"`
}

// BudgetEnvironments is the environments a budget covers; nil covers all.
type BudgetEnvironments struct {
	Allocation string   `json:"allocation"`
	IDs        []string `json:"ids,omitempty"`
}

// BudgetConsumer selects the traffic a budget counts; consumers combine with AND.
type BudgetConsumer struct {
	Type       string  `json:"type"`
	Allocation string  `json:"allocation"`
	ID         *string `json:"id,omitempty"`
	Expression *string `json:"expression,omitempty"`
	// Project is set only in the kind sent to gateways, on a named agent: its project.
	Project *string `json:"project,omitempty"`
}

// BudgetExemption is traffic a budget neither counts nor blocks.
type BudgetExemption struct {
	Type       string  `json:"type"`
	ID         *string `json:"id,omitempty"`
	Project    *string `json:"project,omitempty"`
	Expression *string `json:"expression,omitempty"`
}

// BudgetWarnings is when, and to which notification channels, a budget warns.
type BudgetWarnings struct {
	Thresholds []int32  `json:"thresholds"`
	ChannelIDs []string `json:"channelIds"`
}

// TableName maps Budget to its table.
func (Budget) TableName() string { return "budgets" }
