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
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/schema"
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
	ID        uuid.UUID    `gorm:"column:id;primaryKey"`
	OrgID     string       `gorm:"column:org_id"`
	Origin    BudgetOrigin `gorm:"column:origin"`
	GatewayID *uuid.UUID   `gorm:"column:gateway_id"` // set for gateway budgets only
	ProjectID *uuid.UUID   `gorm:"column:project_id"` // the owning project; nil: the organization
	// Configuration is the kind's spec; the column holds the whole Budget kind as received
	// (Interfaces 1.1), written and read by the budgetkind serializer.
	Configuration BudgetConfig `gorm:"column:configuration;serializer:budgetkind"`
	CreatedBy     string       `gorm:"column:created_by"`

	// Name and the timestamps come from the artifact, the owners' handles from the
	// organization and project, read with joins.
	Name        string    `gorm:"->;column:name"`
	OrgName     string    `gorm:"->;column:org_name"`
	ProjectName string    `gorm:"->;column:project_name"`
	CreatedAt   time.Time `gorm:"->;column:created_at"`
	UpdatedAt   time.Time `gorm:"->;column:updated_at"`
}

// BudgetConfig is a budget's settings (Interfaces 2.2), with defaults filled in: the
// spec of the Budget kind (Interfaces 3).
type BudgetConfig struct {
	DisplayName string            `json:"displayName,omitempty"`
	Description *string           `json:"description,omitempty"`
	Status      string            `json:"status"`
	Limit       BudgetLimit       `json:"limit"`
	Resource    *BudgetResource   `json:"resource,omitempty"`
	Consumers   []BudgetConsumer  `json:"consumers"`
	Exemptions  []BudgetExemption `json:"exemptions"`
	Warnings    *BudgetWarnings   `json:"warnings,omitempty"`
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
	Thresholds []int32 `json:"thresholds"`
	// Channels are notification channel names, as in the kind.
	Channels []string `json:"channels"`
}

// TableName maps Budget to its table.
func (Budget) TableName() string { return "budgets" }

// BudgetAPIVersion is the Budget kind's apiVersion.
const BudgetAPIVersion = "gateway.api-platform.wso2.com/v1"

// AnnotationBudgetProjectID names the project that owns a budget.
const AnnotationBudgetProjectID = "gateway.api-platform.wso2.com/project-id"

// BudgetDocument is a budget as stored, as a gateway stores it: the Budget kind as
// received (Interfaces 1.1, 3).
type BudgetDocument struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   BudgetDocumentMetadata `json:"metadata"`
	Spec       BudgetConfig           `json:"spec"`
}

// BudgetDocumentMetadata names a stored budget and its owning project.
type BudgetDocumentMetadata struct {
	Name        string            `json:"name"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// NewBudgetDocument is the stored kind of a budget with that name, project ("" for an
// organization budget) and spec.
func NewBudgetDocument(name, project string, spec BudgetConfig) BudgetDocument {
	doc := BudgetDocument{APIVersion: BudgetAPIVersion, Kind: KindBudget, Metadata: BudgetDocumentMetadata{Name: name}, Spec: spec}
	if project != "" {
		doc.Metadata.Annotations = map[string]string{AnnotationBudgetProjectID: project}
	}
	return doc
}

func init() {
	schema.RegisterSerializer("budgetkind", budgetKindSerializer{})
}

// budgetKindSerializer stores a Budget's Configuration as the whole Budget kind, named
// after the budget's Name and ProjectName, and reads the spec back. Metadata is read from
// the budget's columns, so the stored annotation never overrides the current project.
type budgetKindSerializer struct{}

func (budgetKindSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	var raw []byte
	switch v := dbValue.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("budget configuration: unsupported type %T", dbValue)
	}
	var probe struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("budget configuration: %w", err)
	}
	var cfg BudgetConfig
	body := raw
	if len(probe.Spec) > 0 {
		body = probe.Spec
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("budget configuration: %w", err)
	}
	return field.Set(ctx, dst, cfg)
}

func (budgetKindSerializer) Value(_ context.Context, _ *schema.Field, dst reflect.Value, fieldValue any) (any, error) {
	cfg, _ := fieldValue.(BudgetConfig)
	name, project := "", ""
	for dst.Kind() == reflect.Pointer {
		dst = dst.Elem()
	}
	if dst.Kind() == reflect.Struct {
		name = dst.FieldByName("Name").String()
		project = dst.FieldByName("ProjectName").String()
	}
	b, err := json.Marshal(NewBudgetDocument(name, project, cfg))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
