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

// BudgetUsage is one budget allowance's spend in its current period, as gateways
// report it (Interfaces 1.2). Spent is a decimal string in USD.
type BudgetUsage struct {
	BudgetID         uuid.UUID         `gorm:"column:budget_id;primaryKey"`
	AllowanceID      uuid.UUID         `gorm:"column:allowance_id;primaryKey"`
	OUID             string            `gorm:"column:org_id"`
	Allowance        map[string]string `gorm:"column:allowance;serializer:json"`
	PeriodStart      time.Time         `gorm:"column:period_start"`
	Spent            string            `gorm:"column:spent"`
	UnpricedRequests int64             `gorm:"column:unpriced_requests"`
	UpdatedAt        time.Time         `gorm:"column:updated_at"`
}

func (BudgetUsage) TableName() string { return "budget_usage" }
