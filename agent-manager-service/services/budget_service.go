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
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/audit"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// Budget field values and limits (Interfaces 2.2).
const (
	budgetAPIVersion         = "gateway.api-platform.wso2.com/v1"
	budgetStatusActive       = "active"
	budgetStatusInactive     = "inactive"
	budgetDefaultPageSize    = 20
	budgetMaxPageSize        = 100
	budgetCurrencyUSD        = "USD"
	budgetActionBlock        = "block"
	budgetActionAllow        = "allow"
	budgetAllocationShared   = "shared"
	budgetAllocationSeparate = "separate"
	budgetDefaultBlockAt     = 100

	budgetTypeLLMProvider = "llmProvider"
	budgetTypeProject     = "project"
	budgetTypeAgent       = "agent"
	budgetTypeAPIKey      = "apiKey"
	budgetTypeLLMProxy    = "llmProxy"
	budgetTypeAttribute   = "attribute"

	maxBudgetDisplayName  = 255
	maxBudgetDescription  = 1023
	maxBudgetEnvironments = 50
	maxBudgetConsumers    = 20
	maxBudgetExemptions   = 100
	maxBudgetThresholds   = 10
	maxBudgetThreshold    = 1000
	maxBudgetChannels     = 20
)

var (
	budgetNamePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	budgetAmountPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`)
	budgetPeriods       = []string{"day", "week", "month"}
	budgetConsumerTypes = []string{budgetTypeProject, budgetTypeAgent, budgetTypeAPIKey, budgetTypeLLMProxy, budgetTypeAttribute}
)

// BudgetError is a budget request the service refuses: Code is the ErrorResponse code,
// Field the payload field it is about (empty when it is about the whole request).
type BudgetError struct {
	Code    string
	Message string
	Field   string
}

func (e *BudgetError) Error() string { return e.Message }

func invalidBudget(field, format string, args ...any) error {
	return &BudgetError{Code: utils.ErrCodeValidation, Message: fmt.Sprintf(format, args...), Field: field}
}

func budgetReferenceNotFound(field, format string, args ...any) error {
	return &BudgetError{Code: utils.ErrCodeReferenceNotFound, Message: fmt.Sprintf(format, args...), Field: field}
}

// BudgetService manages budgets created in the control plane.
type BudgetService interface {
	// Create stores a new, active budget for the organization. createdBy is the caller.
	Create(ctx context.Context, ouID, createdBy string, req spec.BudgetRequest) (*spec.BudgetResponse, error)
	// Get returns the organization's budget named name.
	Get(ctx context.Context, ouID, name string) (*spec.BudgetResponse, error)
	// List returns a page of the organization's budgets matching every filter (type:id).
	List(ctx context.Context, ouID string, filters []string, limit, offset int32) (*spec.BudgetListResponse, error)
	// Update replaces a control-plane budget's editable fields.
	Update(ctx context.Context, ouID, name string, req spec.BudgetUpdateRequest) (*spec.BudgetResponse, error)
	// Delete deletes a control-plane budget.
	Delete(ctx context.Context, ouID, name string) error
}

type budgetService struct {
	budgetRepo   repositories.BudgetRepository
	artifactRepo repositories.ArtifactRepository
	platform     repositories.PlatformRepository
	eventRepo    repositories.EventRepository
}

// NewBudgetService creates a BudgetService.
func NewBudgetService(
	budgetRepo repositories.BudgetRepository,
	artifactRepo repositories.ArtifactRepository,
	platform repositories.PlatformRepository,
	eventRepo repositories.EventRepository,
) BudgetService {
	return &budgetService{budgetRepo: budgetRepo, artifactRepo: artifactRepo, platform: platform, eventRepo: eventRepo}
}

// BudgetKind is the Budget kind (Interfaces 3): what gateways receive and what pipelines
// keep in files. Agent Manager stores only its spec, as BudgetConfig.
type BudgetKind struct {
	APIVersion string              `json:"apiVersion" yaml:"apiVersion"`
	Kind       string              `json:"kind" yaml:"kind"`
	Metadata   BudgetKindMetadata  `json:"metadata" yaml:"metadata"`
	Spec       models.BudgetConfig `json:"spec" yaml:"spec"`
}

// BudgetKindMetadata names the budget.
type BudgetKindMetadata struct {
	Name string `json:"name" yaml:"name"`
}

// BuildBudgetKind wraps a stored budget in the Budget kind. For a gateway it leaves out
// what gateways never get: displayName, description and warnings (Interfaces 3).
func BuildBudgetKind(name string, cfg models.BudgetConfig, forGateway bool) BudgetKind {
	if forGateway {
		cfg.DisplayName, cfg.Description, cfg.Warnings = "", nil, nil
	}
	return BudgetKind{APIVersion: budgetAPIVersion, Kind: models.KindBudget, Metadata: BudgetKindMetadata{Name: name}, Spec: cfg}
}

func (s *budgetService) Create(ctx context.Context, ouID, createdBy string, req spec.BudgetRequest) (*spec.BudgetResponse, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	cfg, err := normalizeBudget(req)
	if err != nil {
		return nil, err
	}
	if err := s.checkReferences(ctx, ouID, cfg); err != nil {
		return nil, err
	}
	budget := &models.Budget{
		OrgID: ouID, Origin: models.BudgetOriginControlPlane, Configuration: *cfg, CreatedBy: createdBy,
	}
	if err := s.budgetRepo.CreateForControlPlane(ctx, budget, req.Name); err != nil {
		if errors.Is(err, utils.ErrBudgetAlreadyExists) {
			return nil, &BudgetError{
				Code: utils.ErrCodeBudgetAlreadyExists, Field: "name",
				Message: fmt.Sprintf("A budget or other resource named %s already exists.", req.Name),
			}
		}
		return nil, err
	}
	return BudgetResponse(budget), nil
}

func budgetNotFound(name string) error {
	return &BudgetError{Code: utils.ErrCodeBudgetNotFound, Message: fmt.Sprintf("Budget %s does not exist.", name)}
}

// getBudget loads a budget, answering BUDGET_NOT_FOUND when there is none.
func (s *budgetService) getBudget(ctx context.Context, ouID, name string) (*models.Budget, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	b, err := s.budgetRepo.Get(ctx, ouID, name)
	if errors.Is(err, utils.ErrBudgetNotFound) {
		return nil, budgetNotFound(name)
	}
	return b, err
}

// writableBudget loads a budget the control plane may change: a gateway's are read-only.
func (s *budgetService) writableBudget(ctx context.Context, ouID, name string) (*models.Budget, error) {
	b, err := s.getBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	if b.Origin != models.BudgetOriginControlPlane {
		return nil, &BudgetError{
			Code:    utils.ErrCodeBudgetReadOnly,
			Message: fmt.Sprintf("Budget %s was created on a gateway and can be changed only there.", name),
		}
	}
	return b, nil
}

func (s *budgetService) Get(ctx context.Context, ouID, name string) (*spec.BudgetResponse, error) {
	b, err := s.getBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	return BudgetResponse(b), nil
}

func (s *budgetService) List(ctx context.Context, ouID string, filters []string, limit, offset int32) (*spec.BudgetListResponse, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	if limit == 0 {
		limit = budgetDefaultPageSize
	}
	if limit < 1 || limit > budgetMaxPageSize {
		return nil, invalidBudget("limit", "limit must be from 1 to %d.", budgetMaxPageSize)
	}
	if offset < 0 {
		return nil, invalidBudget("offset", "offset must not be negative.")
	}
	matchers, err := parseBudgetFilters(filters)
	if err != nil {
		return nil, err
	}
	// An organization has at most a few thousand budgets (Interfaces 2.2), so they are
	// filtered here rather than with database-specific JSON queries.
	all, err := s.budgetRepo.List(ctx, ouID)
	if err != nil {
		return nil, err
	}
	matched := make([]spec.BudgetResponse, 0, len(all))
	for i := range all {
		if budgetMatches(&all[i].Configuration, matchers) {
			matched = append(matched, *BudgetResponse(&all[i]))
		}
	}
	total := int32(len(matched))
	start, end := min(offset, total), min(offset+limit, total)
	return spec.NewBudgetListResponse(matched[start:end], total, limit, offset), nil
}

func (s *budgetService) Update(ctx context.Context, ouID, name string, req spec.BudgetUpdateRequest) (*spec.BudgetResponse, error) {
	b, err := s.writableBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	cfg := b.Configuration
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" || len([]rune(displayName)) > maxBudgetDisplayName {
		return nil, invalidBudget("displayName", "displayName must be 1-%d characters.", maxBudgetDisplayName)
	}
	if req.Description != nil && len([]rune(*req.Description)) > maxBudgetDescription {
		return nil, invalidBudget("description", "description must be at most %d characters.", maxBudgetDescription)
	}
	status := budgetStatusActive
	if req.Status != nil {
		if *req.Status != budgetStatusActive && *req.Status != budgetStatusInactive {
			return nil, invalidBudget("status", "status must be active or inactive.")
		}
		status = *req.Status
	}
	// Currency and period are fixed: the new amount and action keep them.
	currency := cfg.Limit.Currency
	limit, err := normalizeBudgetLimit(spec.BudgetLimit{
		Amount: req.Limit.Amount, Currency: &currency, Period: cfg.Limit.Period, Action: req.Limit.Action, BlockAt: req.Limit.BlockAt,
	})
	if err != nil {
		return nil, err
	}
	exemptions, err := normalizeBudgetExemptions(req.Exemptions)
	if err != nil {
		return nil, err
	}
	warnings, err := normalizeBudgetWarnings(req.Warnings)
	if err != nil {
		return nil, err
	}
	cfg.DisplayName, cfg.Description, cfg.Status = displayName, req.Description, status
	cfg.Limit, cfg.Exemptions, cfg.Warnings = limit, exemptions, warnings
	if err := s.checkEditableReferences(ctx, ouID, &cfg); err != nil {
		return nil, err
	}
	b.Configuration = cfg
	if err := s.budgetRepo.Update(ctx, b); err != nil {
		if errors.Is(err, utils.ErrBudgetNotFound) {
			return nil, budgetNotFound(name)
		}
		return nil, err
	}
	return BudgetResponse(b), nil
}

func (s *budgetService) Delete(ctx context.Context, ouID, name string) error {
	b, err := s.writableBudget(ctx, ouID, name)
	if err != nil {
		return err
	}
	if err := s.budgetRepo.Delete(ctx, ouID, name); err != nil {
		if errors.Is(err, utils.ErrBudgetNotFound) {
			return budgetNotFound(name)
		}
		return err
	}
	audit.Record(
		ctx, audit.ActionBudgetDelete,
		audit.Org(ouID),
		audit.ResourceNamed("budget", b.ID.String(), name),
		audit.Detail("budgetName", name),
	)
	return nil
}

// budgetFilterTypes are the types a list filter can name.
var budgetFilterTypes = []string{budgetTypeProject, budgetTypeAgent, budgetTypeAPIKey, budgetTypeLLMProxy, budgetTypeLLMProvider}

type budgetFilter struct{ typ, id string }

// parseBudgetFilters reads type:id filters (Interfaces 2.1).
func parseBudgetFilters(filters []string) ([]budgetFilter, error) {
	out := make([]budgetFilter, 0, len(filters))
	for i, f := range filters {
		typ, id, ok := strings.Cut(f, ":")
		if !ok || id == "" || !containsString(budgetFilterTypes, typ) {
			return nil, invalidBudget(fmt.Sprintf("filter[%d]", i),
				"filter must be type:id, with type project, agent, apiKey, llmProxy or llmProvider.")
		}
		out = append(out, budgetFilter{typ: typ, id: id})
	}
	return out, nil
}

// budgetMatches reports whether the budget's resource, consumers or exemptions name
// every filter's value.
func budgetMatches(c *models.BudgetConfig, filters []budgetFilter) bool {
	for _, f := range filters {
		if !budgetNames(c, f) {
			return false
		}
	}
	return true
}

func budgetNames(c *models.BudgetConfig, f budgetFilter) bool {
	is := func(p *string) bool { return p != nil && *p == f.id }
	if f.typ == budgetTypeLLMProvider {
		return c.Resource != nil && is(c.Resource.ID)
	}
	for _, x := range c.Consumers {
		if x.Type == f.typ && is(x.ID) {
			return true
		}
	}
	for _, x := range c.Exemptions {
		if (x.Type == f.typ && is(x.ID)) || (f.typ == budgetTypeProject && is(x.Project)) {
			return true
		}
	}
	return false
}

// normalizeBudget checks the payload (Interfaces 2.2) and returns the budget's settings
// with defaults filled in.
func normalizeBudget(req spec.BudgetRequest) (*models.BudgetConfig, error) {
	if !budgetNamePattern.MatchString(req.Name) {
		return nil, invalidBudget("name", "name must be 1-63 lowercase letters, digits or '-', starting and ending with a letter or digit.")
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" || len([]rune(displayName)) > maxBudgetDisplayName {
		return nil, invalidBudget("displayName", "displayName must be 1-%d characters.", maxBudgetDisplayName)
	}
	if req.Description != nil && len([]rune(*req.Description)) > maxBudgetDescription {
		return nil, invalidBudget("description", "description must be at most %d characters.", maxBudgetDescription)
	}
	limit, err := normalizeBudgetLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	resource, err := normalizeBudgetResource(req.Resource)
	if err != nil {
		return nil, err
	}
	environments, err := normalizeBudgetEnvironments(req.Environments)
	if err != nil {
		return nil, err
	}
	consumers, err := normalizeBudgetConsumers(req.Consumers)
	if err != nil {
		return nil, err
	}
	exemptions, err := normalizeBudgetExemptions(req.Exemptions)
	if err != nil {
		return nil, err
	}
	warnings, err := normalizeBudgetWarnings(req.Warnings)
	if err != nil {
		return nil, err
	}
	return &models.BudgetConfig{
		DisplayName: displayName, Description: req.Description, Status: budgetStatusActive,
		Limit: limit, Resource: resource, Environments: environments,
		Consumers: consumers, Exemptions: exemptions, Warnings: warnings,
	}, nil
}

func normalizeBudgetLimit(in spec.BudgetLimit) (models.BudgetLimit, error) {
	out := models.BudgetLimit{Amount: in.Amount, Currency: budgetCurrencyUSD, Period: in.Period, Action: in.Action}
	amount, ok := new(big.Rat).SetString(in.Amount)
	if !budgetAmountPattern.MatchString(in.Amount) || !ok || amount.Sign() <= 0 {
		return out, invalidBudget("limit.amount", "limit.amount must be a decimal above zero with at most 2 decimals, e.g. \"100.00\".")
	}
	if in.Currency != nil && *in.Currency != budgetCurrencyUSD {
		return out, invalidBudget("limit.currency", "limit.currency must be USD.")
	}
	if !containsString(budgetPeriods, in.Period) {
		return out, invalidBudget("limit.period", "limit.period must be day, week or month.")
	}
	switch in.Action {
	case budgetActionBlock:
		blockAt := int32(budgetDefaultBlockAt)
		if in.BlockAt != nil {
			if *in.BlockAt < 1 || *in.BlockAt > 100 {
				return out, invalidBudget("limit.blockAt", "limit.blockAt must be a whole percentage from 1 to 100.")
			}
			blockAt = *in.BlockAt
		}
		out.BlockAt = &blockAt
	case budgetActionAllow:
		if in.BlockAt != nil {
			return out, invalidBudget("limit.blockAt", "limit.blockAt applies only to the block action.")
		}
	default:
		return out, invalidBudget("limit.action", "limit.action must be block or allow.")
	}
	return out, nil
}

func normalizeAllocation(field string, in *string) (string, error) {
	if in == nil {
		return budgetAllocationShared, nil
	}
	if *in != budgetAllocationShared && *in != budgetAllocationSeparate {
		return "", invalidBudget(field, "%s must be shared or separate.", field)
	}
	return *in, nil
}

func normalizeBudgetResource(in *spec.BudgetResource) (*models.BudgetResource, error) {
	if in == nil {
		return nil, nil //nolint:nilnil // no resource: the budget covers all providers, shared
	}
	if in.Type != budgetTypeLLMProvider {
		return nil, invalidBudget("resource.type", "resource.type must be llmProvider.")
	}
	allocation, err := normalizeAllocation("resource.allocation", in.Allocation)
	if err != nil {
		return nil, err
	}
	if allocation == budgetAllocationSeparate && in.Id != nil {
		return nil, invalidBudget("resource.id", "resource.id applies only to a shared resource.")
	}
	return &models.BudgetResource{Type: in.Type, Allocation: allocation, ID: in.Id}, nil
}

func normalizeBudgetEnvironments(in *spec.BudgetEnvironments) (*models.BudgetEnvironments, error) {
	if in == nil {
		return nil, nil //nolint:nilnil // no environments: all of them, including new ones
	}
	allocation, err := normalizeAllocation("environments.allocation", in.Allocation)
	if err != nil {
		return nil, err
	}
	if len(in.Ids) > maxBudgetEnvironments {
		return nil, invalidBudget("environments.ids", "environments.ids takes at most %d environments.", maxBudgetEnvironments)
	}
	seen := map[string]bool{}
	for i, id := range in.Ids {
		if id == "" || seen[id] {
			return nil, invalidBudget(fmt.Sprintf("environments.ids[%d]", i), "Environment names must be non-empty and unique.")
		}
		seen[id] = true
	}
	return &models.BudgetEnvironments{Allocation: allocation, IDs: in.Ids}, nil
}

func normalizeBudgetConsumers(in []spec.BudgetConsumer) ([]models.BudgetConsumer, error) {
	if len(in) > maxBudgetConsumers {
		return nil, invalidBudget("consumers", "consumers takes at most %d entries.", maxBudgetConsumers)
	}
	out := make([]models.BudgetConsumer, 0, len(in))
	seen := map[string]bool{}
	hasNamedProject := false
	for i, c := range in {
		field := fmt.Sprintf("consumers[%d]", i)
		if !containsString(budgetConsumerTypes, c.Type) {
			return nil, invalidBudget(field+".type", "Consumer type must be project, agent, apiKey, llmProxy or attribute.")
		}
		if c.Type != budgetTypeAttribute {
			if seen[c.Type] {
				return nil, invalidBudget(field+".type", "Each consumer type except attribute can be used once.")
			}
			seen[c.Type] = true
		}
		allocation, err := normalizeAllocation(field+".allocation", c.Allocation)
		if err != nil {
			return nil, err
		}
		if c.Type == budgetTypeAttribute {
			if c.Expression == nil || strings.TrimSpace(*c.Expression) == "" {
				return nil, invalidBudget(field+".expression", "An attribute consumer needs an expression.")
			}
			if c.Id != nil {
				return nil, invalidBudget(field+".id", "An attribute consumer takes an expression, not an id.")
			}
		} else {
			if c.Expression != nil {
				return nil, invalidBudget(field+".expression", "Only an attribute consumer takes an expression.")
			}
			if allocation == budgetAllocationShared && (c.Id == nil || *c.Id == "") {
				return nil, invalidBudget(field+".id", "A shared %s consumer needs an id.", c.Type)
			}
			if allocation == budgetAllocationSeparate && c.Id != nil {
				return nil, invalidBudget(field+".id", "A separate %s consumer takes no id.", c.Type)
			}
		}
		if c.Type == budgetTypeProject && c.Id != nil {
			hasNamedProject = true
		}
		out = append(out, models.BudgetConsumer{Type: c.Type, Allocation: allocation, ID: c.Id, Expression: c.Expression})
	}
	for i, c := range out {
		// An agent name is unique only within its project.
		if c.Type == budgetTypeAgent && c.ID != nil && !hasNamedProject {
			return nil, invalidBudget(fmt.Sprintf("consumers[%d].id", i), "A named agent needs the shared project consumer of its project.")
		}
	}
	return out, nil
}

func normalizeBudgetExemptions(in []spec.BudgetExemption) ([]models.BudgetExemption, error) {
	if len(in) > maxBudgetExemptions {
		return nil, invalidBudget("exemptions", "exemptions takes at most %d entries.", maxBudgetExemptions)
	}
	out := make([]models.BudgetExemption, 0, len(in))
	empty := func(p *string) bool { return p == nil || *p == "" }
	for i, e := range in {
		field := fmt.Sprintf("exemptions[%d]", i)
		switch e.Type {
		case budgetTypeProject:
			if empty(e.Id) || e.Project != nil || e.Expression != nil {
				return nil, invalidBudget(field, "A project exemption takes only an id.")
			}
		case budgetTypeAgent:
			if empty(e.Id) || empty(e.Project) || e.Expression != nil {
				return nil, invalidBudget(field, "An agent exemption takes an id and its project.")
			}
		case budgetTypeAttribute:
			if empty(e.Expression) || e.Id != nil || e.Project != nil {
				return nil, invalidBudget(field, "An attribute exemption takes only an expression.")
			}
		default:
			return nil, invalidBudget(field+".type", "Exemption type must be project, agent or attribute.")
		}
		out = append(out, models.BudgetExemption{Type: e.Type, ID: e.Id, Project: e.Project, Expression: e.Expression})
	}
	return out, nil
}

func normalizeBudgetWarnings(in *spec.BudgetWarnings) (*models.BudgetWarnings, error) {
	if in == nil {
		return nil, nil //nolint:nilnil // warnings are optional
	}
	if len(in.Thresholds) < 1 || len(in.Thresholds) > maxBudgetThresholds {
		return nil, invalidBudget("warnings.thresholds", "warnings.thresholds takes 1-%d percentages.", maxBudgetThresholds)
	}
	seenThreshold := map[int32]bool{}
	for i, t := range in.Thresholds {
		if t < 1 || t > maxBudgetThreshold || seenThreshold[t] {
			return nil, invalidBudget(fmt.Sprintf("warnings.thresholds[%d]", i),
				"Thresholds must be unique whole percentages from 1 to %d.", maxBudgetThreshold)
		}
		seenThreshold[t] = true
	}
	if len(in.ChannelIds) < 1 || len(in.ChannelIds) > maxBudgetChannels {
		return nil, invalidBudget("warnings.channelIds", "warnings.channelIds takes 1-%d channels.", maxBudgetChannels)
	}
	// Stored in canonical form, as monitor alerts store destination IDs.
	channels := make([]string, 0, len(in.ChannelIds))
	seenChannel := map[uuid.UUID]bool{}
	for i, raw := range in.ChannelIds {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || seenChannel[id] {
			return nil, invalidBudget(fmt.Sprintf("warnings.channelIds[%d]", i), "Channel IDs must be valid and unique.")
		}
		seenChannel[id] = true
		channels = append(channels, id.String())
	}
	return &models.BudgetWarnings{Thresholds: in.Thresholds, ChannelIDs: channels}, nil
}

// checkReferences checks that the providers, projects, agents, LLM proxies, environments
// and channels the budget names exist in the organization.
func (s *budgetService) checkReferences(ctx context.Context, ouID string, b *models.BudgetConfig) error {
	if b.Resource != nil && b.Resource.ID != nil {
		if err := s.checkArtifact(ouID, models.KindLLMProvider, *b.Resource.ID, "resource.id", "LLM provider"); err != nil {
			return err
		}
	}
	if b.Environments != nil {
		for i, name := range b.Environments.IDs {
			env, err := s.platform.GetEnvironment(ctx, ouID, name)
			if err != nil && !errors.Is(err, utils.ErrEnvironmentNotFound) {
				return fmt.Errorf("failed to get environment %q: %w", name, err)
			}
			if err != nil || env.Name != name {
				return budgetReferenceNotFound(fmt.Sprintf("environments.ids[%d]", i), "Environment %s does not exist.", name)
			}
		}
	}
	project := budgetProject(b)
	for i, c := range b.Consumers {
		if c.ID == nil {
			continue
		}
		field := fmt.Sprintf("consumers[%d].id", i)
		var err error
		switch c.Type {
		case budgetTypeProject:
			err = s.checkProject(ctx, ouID, *c.ID, field)
		case budgetTypeAgent:
			err = s.checkAgent(ctx, ouID, project, *c.ID, field)
		case budgetTypeLLMProxy:
			err = s.checkArtifact(ouID, models.KindLLMProxy, *c.ID, field, "LLM proxy")
		}
		if err != nil {
			return err
		}
	}
	return s.checkEditableReferences(ctx, ouID, b)
}

// budgetProject is the project a budget's shared project consumer names, if any.
func budgetProject(b *models.BudgetConfig) string {
	for _, c := range b.Consumers {
		if c.Type == budgetTypeProject && c.ID != nil {
			return *c.ID
		}
	}
	return ""
}

// checkEditableReferences checks the references an update can change: the exempt
// projects and agents, and the warning channels.
func (s *budgetService) checkEditableReferences(ctx context.Context, ouID string, b *models.BudgetConfig) error {
	for i, e := range b.Exemptions {
		field := fmt.Sprintf("exemptions[%d].id", i)
		var err error
		switch e.Type {
		case budgetTypeProject:
			err = s.checkProject(ctx, ouID, *e.ID, field)
		case budgetTypeAgent:
			err = s.checkAgent(ctx, ouID, *e.Project, *e.ID, field)
		}
		if err != nil {
			return err
		}
	}
	if b.Warnings != nil {
		return s.checkChannels(ctx, ouID, budgetProject(b), b.Warnings.ChannelIDs)
	}
	return nil
}

func (s *budgetService) checkArtifact(ouID, kind, handle, field, what string) error {
	exists, err := s.artifactRepo.Exists(kind, handle, ouID)
	if err != nil {
		return fmt.Errorf("failed to check %s %q: %w", what, handle, err)
	}
	if !exists {
		return budgetReferenceNotFound(field, "%s %s does not exist.", what, handle)
	}
	return nil
}

func (s *budgetService) checkProject(ctx context.Context, ouID, name, field string) error {
	if _, err := s.platform.GetProject(ctx, ouID, name); err != nil {
		if errors.Is(err, utils.ErrProjectNotFound) {
			return budgetReferenceNotFound(field, "Project %s does not exist.", name)
		}
		return fmt.Errorf("failed to get project %q: %w", name, err)
	}
	return nil
}

func (s *budgetService) checkAgent(ctx context.Context, ouID, project, name, field string) error {
	if _, err := s.platform.GetAgent(ctx, ouID, project, name); err != nil {
		if errors.Is(err, utils.ErrAgentNotFound) || errors.Is(err, utils.ErrProjectNotFound) {
			return budgetReferenceNotFound(field, "Agent %s does not exist in project %s.", name, project)
		}
		return fmt.Errorf("failed to get agent %q: %w", name, err)
	}
	return nil
}

// checkChannels checks each warning channel is available to the budget (Interfaces 2.4):
// availability all or org, or projects when the budget's project consumer is one of them.
func (s *budgetService) checkChannels(ctx context.Context, ouID, project string, ids []string) error {
	for i, raw := range ids {
		field := fmt.Sprintf("warnings.channelIds[%d]", i)
		unavailable := &BudgetError{
			Code: utils.ErrCodeChannelNotAvailable, Field: field,
			Message: fmt.Sprintf("Notification channel %s is not available to this budget.", raw),
		}
		destination, err := s.eventRepo.GetDestination(ctx, ouID, uuid.MustParse(raw))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return unavailable
			}
			return fmt.Errorf("failed to get notification channel %s: %w", raw, err)
		}
		if destination.OUID != ouID {
			return unavailable
		}
		switch destination.Availability {
		case models.DestinationAvailableAll, models.DestinationAvailableOrg:
		case models.DestinationAvailableProjects:
			if project == "" || !containsString(destination.Projects, project) {
				return unavailable
			}
		default:
			return unavailable
		}
	}
	return nil
}

// BudgetResponse is a stored budget as the API returns it (Interfaces 2.3).
func BudgetResponse(b *models.Budget) *spec.BudgetResponse {
	c := b.Configuration
	consumers := make([]spec.BudgetConsumer, 0, len(c.Consumers))
	for _, x := range c.Consumers {
		allocation := x.Allocation
		consumers = append(consumers, spec.BudgetConsumer{Type: x.Type, Allocation: &allocation, Id: x.ID, Expression: x.Expression})
	}
	exemptions := make([]spec.BudgetExemption, 0, len(c.Exemptions))
	for _, x := range c.Exemptions {
		exemptions = append(exemptions, spec.BudgetExemption{Type: x.Type, Id: x.ID, Project: x.Project, Expression: x.Expression})
	}
	currency := c.Limit.Currency
	limit := spec.BudgetLimit{Amount: c.Limit.Amount, Currency: &currency, Period: c.Limit.Period, Action: c.Limit.Action, BlockAt: c.Limit.BlockAt}
	resp := spec.NewBudgetResponse(b.Name, c.DisplayName, c.Status, string(b.Origin), limit, consumers, exemptions,
		b.CreatedAt, b.UpdatedAt)
	resp.Description = c.Description
	if c.Resource != nil {
		allocation := c.Resource.Allocation
		resp.Resource = &spec.BudgetResource{Type: c.Resource.Type, Allocation: &allocation, Id: c.Resource.ID}
	}
	if c.Environments != nil {
		allocation := c.Environments.Allocation
		resp.Environments = &spec.BudgetEnvironments{Allocation: &allocation, Ids: c.Environments.IDs}
	}
	if c.Warnings != nil {
		resp.Warnings = &spec.BudgetWarnings{Thresholds: c.Warnings.Thresholds, ChannelIds: c.Warnings.ChannelIDs}
	}
	if b.CreatedBy != "" {
		resp.CreatedBy = &b.CreatedBy
	}
	return resp
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
