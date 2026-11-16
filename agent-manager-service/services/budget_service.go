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
	"io"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wso2/agent-manager/agent-manager-service/audit"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/orgctx"
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
	budgetTypeAttribute   = "attribute"

	maxBudgetDisplayName = 255
	maxBudgetDescription = 1023
	maxBudgetConsumers   = 20
	maxBudgetExemptions  = 100
	maxBudgetThresholds  = 10
	maxBudgetThreshold   = 1000
	maxBudgetChannels    = 20
)

// gatewayConsumerTypes are the consumer and exemption types gateways can identify: token
// claims (attribute); the project and agent of an agent's hidden LLM proxy, from its
// annotations; and the API key a request authenticated with. A type not listed here is
// refused (UNSUPPORTED_CONSUMER).
var gatewayConsumerTypes = []string{budgetTypeAttribute, budgetTypeProject, budgetTypeAgent, budgetTypeAPIKey}

var (
	budgetNamePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	budgetAmountPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`)
	budgetPeriods       = []string{"day", "week", "month"}
	budgetConsumerTypes = []string{budgetTypeProject, budgetTypeAgent, budgetTypeAPIKey, budgetTypeAttribute}
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

func unsupportedConsumer(field, typ string) error {
	return &BudgetError{
		Code: utils.ErrCodeUnsupportedConsumer, Field: field,
		Message: fmt.Sprintf("Gateways cannot identify %s consumers yet; use an attribute (token claim) instead.", typ),
	}
}

func budgetReferenceNotFound(field, format string, args ...any) error {
	return &BudgetError{Code: utils.ErrCodeReferenceNotFound, Message: fmt.Sprintf(format, args...), Field: field}
}

// BudgetService manages budgets created in the control plane.
type BudgetService interface {
	// Create stores a new, active budget for the organization. createdBy is the caller.
	Create(ctx context.Context, ouID, createdBy string, kind spec.Budget) (*spec.Budget, error)
	// Import creates the budgets of a CSV file, one per row, all or none (Interfaces 2.5).
	// onExisting says what a row naming an existing budget does: fail (default), skip or
	// update.
	// The result lists each row's errors; applied says whether they were saved. A file that
	// cannot be read is a *BudgetError with code INVALID_CSV.
	Import(ctx context.Context, ouID, createdBy string, file io.Reader, dryRun bool, onExisting string) (*spec.BudgetImportResult, error)
	// Get returns the organization's budget named name.
	Get(ctx context.Context, ouID, name string) (*spec.Budget, error)
	// List returns a page of the organization's budgets matching every filter (type:id),
	// only those the project owns when projectID is set.
	List(ctx context.Context, ouID, projectID string, filters []string, limit, offset int32) (*spec.BudgetListResponse, error)
	// Update replaces a budget's editable fields. A gateway's budget changes only in the
	// fields gateways never get (displayName, description, warnings).
	Update(ctx context.Context, ouID, name string, kind spec.Budget) (*spec.Budget, error)
	// Usage returns a page of the budget's allowances with their spend this period,
	// highest first (GET /budgets/{budgetName}/budget-usage).
	Usage(ctx context.Context, ouID, name string, limit, offset int32) (*spec.BudgetUsageListResponse, error)
	// Delete deletes a control-plane budget.
	Delete(ctx context.Context, ouID, name string) error
}

type budgetService struct {
	budgetRepo   repositories.BudgetRepository
	artifactRepo repositories.ArtifactRepository
	platform     repositories.PlatformRepository
	eventRepo    repositories.EventRepository
	gateways     BudgetGatewaySender
	warner       BudgetWarner
	usageRepo    repositories.BudgetUsageRepository
	now          func() time.Time
}

// NewBudgetService creates a BudgetService. gateways sends budgets to the gateways that
// enforce them; warner checks an edited budget's thresholds against its spend; usageRepo
// holds the spend the gateways report.
func NewBudgetService(
	budgetRepo repositories.BudgetRepository,
	artifactRepo repositories.ArtifactRepository,
	platform repositories.PlatformRepository,
	eventRepo repositories.EventRepository,
	gateways BudgetGatewaySender,
	warner BudgetWarner,
	usageRepo repositories.BudgetUsageRepository,
) BudgetService {
	return &budgetService{
		budgetRepo: budgetRepo, artifactRepo: artifactRepo, platform: platform, eventRepo: eventRepo,
		gateways: gateways, warner: warner, usageRepo: usageRepo, now: time.Now,
	}
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
		// A gateway names an agent with its project (agent names are unique per project);
		// the budget's project consumer is the project of a named agent.
		project := ""
		for _, c := range cfg.Consumers {
			if c.Type == budgetTypeProject && c.ID != nil {
				project = *c.ID
			}
		}
		consumers := make([]models.BudgetConsumer, len(cfg.Consumers))
		for i, c := range cfg.Consumers {
			if c.Type == budgetTypeAgent && c.ID != nil && project != "" {
				p := project
				c.Project = &p
			}
			consumers[i] = c
		}
		cfg.Consumers = consumers
		cfg.DisplayName, cfg.Description, cfg.Warnings = "", nil, nil
		if cfg.Resource == nil {
			// No resource covers every provider, shared; the gateway kind names it.
			cfg.Resource = &models.BudgetResource{Type: budgetTypeLLMProvider, Allocation: budgetAllocationShared}
		}
	}
	return BudgetKind{APIVersion: budgetAPIVersion, Kind: models.KindBudget, Metadata: BudgetKindMetadata{Name: name}, Spec: cfg}
}

// budgetRequest is a new budget's fields: from a Budget kind (Create, Update) or a CSV
// row (Import).
type budgetRequest struct {
	Name        string
	ProjectId   *string
	DisplayName *string
	Description *string
	Status      *string
	Limit       spec.BudgetLimit
	Resource    *spec.BudgetResource
	Consumers   []spec.BudgetConsumer
	Exemptions  []spec.BudgetExemption
	Warnings    *spec.BudgetWarnings
}

// budgetUpdateRequest is what an update may change; the other fields are fixed after creation.
type budgetUpdateRequest struct {
	DisplayName *string
	Description *string
	Status      *string
	Limit       budgetUpdateLimit
	Exemptions  []spec.BudgetExemption
	Warnings    *spec.BudgetWarnings
}

// budgetUpdateLimit is the editable part of the limit; currency and period are fixed.
type budgetUpdateLimit struct {
	Amount  string
	Action  string
	BlockAt *int32
}

// budgetFromKind reads a Budget kind sent to the API (Interfaces 3): the project-id
// annotation names the owning project; the organization is the one in the path.
func budgetFromKind(kind spec.Budget) (budgetRequest, error) {
	if kind.ApiVersion != budgetAPIVersion {
		return budgetRequest{}, invalidBudget("apiVersion", "apiVersion must be %s.", budgetAPIVersion)
	}
	if kind.Kind != models.KindBudget {
		return budgetRequest{}, invalidBudget("kind", "kind must be %s.", models.KindBudget)
	}
	req := budgetRequest{
		Name: kind.Metadata.Name, DisplayName: kind.Spec.DisplayName, Description: kind.Spec.Description,
		Status: kind.Spec.Status, Limit: kind.Spec.Limit, Resource: kind.Spec.Resource,
		Consumers: kind.Spec.Consumers, Exemptions: kind.Spec.Exemptions, Warnings: kind.Spec.Warnings,
	}
	if kind.Metadata.Annotations != nil {
		if project, ok := (*kind.Metadata.Annotations)[AnnotationProjectID]; ok {
			project = strings.TrimSpace(project)
			if project == "" {
				return budgetRequest{}, invalidBudget("metadata.annotations",
					"%s must name a project; leave it out for an organization budget.", AnnotationProjectID)
			}
			req.ProjectId = &project
		}
	}
	return req, nil
}

func (s *budgetService) Create(ctx context.Context, ouID, createdBy string, kind spec.Budget) (*spec.Budget, error) {
	req, err := budgetFromKind(kind)
	if err != nil {
		return nil, err
	}
	return s.create(ctx, ouID, createdBy, req)
}

func (s *budgetService) create(ctx context.Context, ouID, createdBy string, req budgetRequest) (*spec.Budget, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	budget, err := s.prepareBudget(ctx, ouID, createdBy, req)
	if err != nil {
		return nil, err
	}
	if err := s.budgetRepo.CreateForControlPlane(ctx, budget, req.Name); err != nil {
		if errors.Is(err, utils.ErrBudgetAlreadyExists) {
			return nil, budgetNameTaken(req.Name)
		}
		return nil, err
	}
	s.gateways.SendBudget(ctx, budget, true)
	return s.response(ctx, ouID, budget)
}

// prepareBudget checks a new budget (Interfaces 2.2), its owner and every name it refers
// to, and returns it ready to store; Create and Import share it.
func (s *budgetService) prepareBudget(ctx context.Context, ouID, createdBy string, req budgetRequest) (*models.Budget, error) {
	req.Consumers = withProjectConsumer(req.Consumers, req.ProjectId)
	cfg, err := normalizeBudget(req)
	if err != nil {
		return nil, err
	}
	orgName, err := s.orgHandle(ctx, ouID)
	if err != nil {
		return nil, err
	}
	var projectID *uuid.UUID
	projectName := ""
	if req.ProjectId != nil {
		projectName = strings.TrimSpace(*req.ProjectId)
		if projectID, err = s.projectID(ctx, ouID, projectName, "metadata.annotations"); err != nil {
			return nil, err
		}
		if err := ownByProject(cfg, projectName); err != nil {
			return nil, err
		}
	}
	if err := s.checkReferences(ctx, ouID, cfg); err != nil {
		return nil, err
	}
	return &models.Budget{
		OrgID: ouID, Origin: models.BudgetOriginControlPlane, ProjectID: projectID, Configuration: *cfg, CreatedBy: createdBy,
		OrgName: orgName, ProjectName: projectName,
	}, nil
}

func budgetNameTaken(name string) error {
	return &BudgetError{
		Code: utils.ErrCodeBudgetAlreadyExists, Field: "name",
		Message: fmt.Sprintf("A budget or other resource named %s already exists.", name),
	}
}

// orgHandle returns the organization's handle: the token's, or the stored one.
func (s *budgetService) orgHandle(ctx context.Context, ouID string) (string, error) {
	if org, ok := orgctx.GetResolvedOrg(ctx); ok && org.OUID == ouID {
		return org.OuHandle, nil
	}
	org, err := s.platform.GetOrganization(ctx, ouID)
	if err != nil {
		return "", fmt.Errorf("failed to get organization: %w", err)
	}
	return org.Name, nil
}

// projectID returns the ID of the organization's project with that name.
func (s *budgetService) projectID(ctx context.Context, ouID, name, field string) (*uuid.UUID, error) {
	return budgetProjectID(ctx, s.platform, ouID, name, field)
}

// budgetProjectID returns the ID of the organization's project with that name, or
// REFERENCE_NOT_FOUND on field when there is none.
func budgetProjectID(ctx context.Context, platform repositories.PlatformRepository, ouID, name, field string) (*uuid.UUID, error) {
	p, err := platform.GetProject(ctx, ouID, name)
	if err != nil {
		if errors.Is(err, utils.ErrProjectNotFound) {
			return nil, budgetReferenceNotFound(field, "Project %s does not exist.", name)
		}
		return nil, fmt.Errorf("failed to get project %q: %w", name, err)
	}
	id, err := uuid.Parse(p.UUID)
	if err != nil {
		return nil, fmt.Errorf("project %q has an invalid ID: %w", name, err)
	}
	return &id, nil
}

// withProjectConsumer adds the owning project as the first consumer, shared, when a
// project budget (the project-id annotation) lists no project consumer: a project budget
// counts only its project's traffic. The stored and returned kind lists it, so gateways
// get it too. A project consumer the request lists is kept as sent; ownByProject checks it.
func withProjectConsumer(consumers []spec.BudgetConsumer, project *string) []spec.BudgetConsumer {
	if project == nil || strings.TrimSpace(*project) == "" {
		return consumers
	}
	for _, c := range consumers {
		if c.Type == budgetTypeProject {
			return consumers
		}
	}
	id, allocation := strings.TrimSpace(*project), budgetAllocationShared
	return append([]spec.BudgetConsumer{{Type: budgetTypeProject, Allocation: &allocation, Id: &id}}, consumers...)
}

// ownByProject checks a budget owned by a project (the project-id annotation) is that
// project's own (Interfaces 2.2): it lists that project as a shared consumer, so it counts
// only that project's traffic, and it exempts only agents of that project. Create adds
// the consumer when it is left out (withProjectConsumer); a gateway's budget must list it.
func ownByProject(cfg *models.BudgetConfig, project string) error {
	listed := false
	for i, c := range cfg.Consumers {
		if c.Type != budgetTypeProject {
			continue
		}
		if c.Allocation != budgetAllocationShared || c.ID == nil || *c.ID != project {
			return invalidBudget(fmt.Sprintf("consumers[%d]", i),
				"A budget of project %s counts only that project: its only project consumer is that project, shared.", project)
		}
		listed = true
	}
	for i, e := range cfg.Exemptions {
		field := fmt.Sprintf("exemptions[%d]", i)
		if e.Type == budgetTypeProject {
			return invalidBudget(field+".type", "A budget of project %s cannot exempt a project.", project)
		}
		if e.Type == budgetTypeAgent && (e.Project == nil || *e.Project != project) {
			return invalidBudget(field+".project", "A budget of project %s can exempt only its agents.", project)
		}
	}
	if !listed {
		return invalidBudget("consumers",
			"A budget of project %s must list that project as a consumer: {type: project, id: %s}.", project, project)
	}
	return nil
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

func (s *budgetService) Get(ctx context.Context, ouID, name string) (*spec.Budget, error) {
	b, err := s.getBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	return s.response(ctx, ouID, b)
}

func (s *budgetService) List(ctx context.Context, ouID, projectID string, filters []string, limit, offset int32) (*spec.BudgetListResponse, error) {
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
	matched := make([]*models.Budget, 0, len(all))
	for i := range all {
		if projectID != "" && all[i].ProjectName != projectID {
			continue
		}
		if budgetMatches(&all[i].Configuration, matchers) {
			matched = append(matched, &all[i])
		}
	}
	total := int32(len(matched))
	start, end := min(offset, total), min(offset+limit, total)
	page, err := s.responses(ctx, ouID, matched[start:end])
	if err != nil {
		return nil, err
	}
	return spec.NewBudgetListResponse(page, total, limit, offset), nil
}

// response is a budget as the API returns it, with its spend this period.
func (s *budgetService) response(ctx context.Context, ouID string, b *models.Budget) (*spec.Budget, error) {
	resps, err := s.responses(ctx, ouID, []*models.Budget{b})
	if err != nil {
		return nil, err
	}
	return &resps[0], nil
}

// responses are budgets as the API returns them, each with its spend this period
// (currentPeriod), read in one query.
func (s *budgetService) responses(ctx context.Context, ouID string, budgets []*models.Budget) ([]spec.Budget, error) {
	out := make([]spec.Budget, len(budgets))
	ids := make([]uuid.UUID, len(budgets))
	for i, b := range budgets {
		out[i] = *BudgetAsKind(b)
		ids[i] = b.ID
	}
	var rows []models.BudgetUsage
	if s.usageRepo != nil && len(ids) > 0 {
		var err error
		if rows, err = s.usageRepo.ForBudgets(ctx, ouID, ids); err != nil {
			return nil, err
		}
	}
	byBudget := map[uuid.UUID][]models.BudgetUsage{}
	for _, r := range rows {
		byBudget[r.BudgetID] = append(byBudget[r.BudgetID], r)
	}
	now := s.now()
	for i, b := range budgets {
		start := budgetPeriodStart(b.Configuration.Limit.Period, now)
		spent, unpriced := new(big.Rat), int64(0)
		for _, r := range byBudget[b.ID] {
			rowSpent, rowUnpriced := spendIn(r, start)
			spent.Add(spent, rowSpent)
			unpriced += rowUnpriced
		}
		current := spec.NewBudgetCurrentPeriod(start.Format(time.DateOnly), spent.FloatString(2), int32(min(unpriced, math.MaxInt32)))
		out[i].Status.CurrentPeriod = current
	}
	return out, nil
}

// budgetPeriodStart is the start of the budget period that holds now: the UTC day, the
// week from Monday, or the month from the 1st (Interfaces 2.2).
func budgetPeriodStart(period string, now time.Time) time.Time {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "day":
		return day
	case "week":
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	default:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
}

// spendIn is an allowance's spend in the period starting at start: its stored totals, or
// nothing when they belong to an earlier period (Interfaces 1.2).
func spendIn(r models.BudgetUsage, start time.Time) (*big.Rat, int64) {
	if !r.PeriodStart.UTC().Equal(start) {
		return new(big.Rat), 0
	}
	spent, ok := new(big.Rat).SetString(r.Spent)
	if !ok {
		spent = new(big.Rat)
	}
	return spent, r.UnpricedRequests
}

func (s *budgetService) Usage(ctx context.Context, ouID, name string, limit, offset int32) (*spec.BudgetUsageListResponse, error) {
	if limit == 0 {
		limit = budgetDefaultPageSize
	}
	if limit < 1 || limit > budgetMaxPageSize {
		return nil, invalidBudget("limit", "limit must be from 1 to %d.", budgetMaxPageSize)
	}
	if offset < 0 {
		return nil, invalidBudget("offset", "offset must not be negative.")
	}
	b, err := s.getBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	rows, err := s.usageRepo.ForBudget(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	start := budgetPeriodStart(b.Configuration.Limit.Period, s.now())
	type usage struct {
		row   models.BudgetUsage
		spent *big.Rat
		label string
	}
	all := make([]usage, 0, len(rows))
	for _, r := range rows {
		spent, unpriced := spendIn(r, start)
		r.UnpricedRequests = unpriced
		all = append(all, usage{row: r, spent: spent, label: allowanceLabel(r.Allowance)})
	}
	// Highest spend first; equal spend by allowance, so pages are stable.
	sort.SliceStable(all, func(i, j int) bool {
		if c := all[i].spent.Cmp(all[j].spent); c != 0 {
			return c > 0
		}
		return all[i].label < all[j].label
	})
	total := int32(len(all))
	from, to := min(offset, total), min(offset+limit, total)
	page := make([]spec.BudgetAllowanceUsage, 0, to-from)
	for _, u := range all[from:to] {
		allowance := u.row.Allowance
		if allowance == nil {
			allowance = map[string]string{}
		}
		page = append(page, *spec.NewBudgetAllowanceUsage(allowance, u.spent.FloatString(2), u.row.UnpricedRequests))
	}
	return spec.NewBudgetUsageListResponse(start.Format(time.DateOnly), page, total, limit, offset), nil
}

func (s *budgetService) Update(ctx context.Context, ouID, name string, kind spec.Budget) (*spec.Budget, error) {
	req, err := budgetFromKind(kind)
	if err != nil {
		return nil, err
	}
	if req.Name != name {
		return nil, invalidBudget("metadata.name", "metadata.name must be %s, the budget being updated; a budget cannot be renamed.", name)
	}
	b, err := s.getBudget(ctx, ouID, name)
	if err != nil {
		return nil, err
	}
	if err := fixedFieldsUnchanged(b, req); err != nil {
		return nil, err
	}
	return s.update(ctx, ouID, b, budgetUpdateRequest{
		DisplayName: req.DisplayName, Description: req.Description, Status: req.Status,
		Limit:      budgetUpdateLimit{Amount: req.Limit.Amount, Action: req.Limit.Action, BlockAt: req.Limit.BlockAt},
		Exemptions: req.Exemptions, Warnings: req.Warnings,
	})
}

// fixedFieldsUnchanged checks an update sends the fields fixed after creation as stored:
// the owning project, currency, period, resource and consumers (Interfaces 2.2). A project
// budget may leave out its project consumer, as on create.
func fixedFieldsUnchanged(b *models.Budget, req budgetRequest) error {
	fixed := func(field, what string) error {
		return invalidBudget(field, "%s cannot change after creation; create a new budget instead.", what)
	}
	project := ""
	if req.ProjectId != nil {
		project = *req.ProjectId
	}
	if project != b.ProjectName {
		return fixed("metadata.annotations", "The budget's project ("+AnnotationProjectID+")")
	}
	stored := b.Configuration
	if req.Limit.Currency != nil && *req.Limit.Currency != stored.Limit.Currency {
		return fixed("spec.limit.currency", "currency")
	}
	if req.Limit.Period != stored.Limit.Period {
		return fixed("spec.limit.period", "period")
	}
	resource, err := normalizeBudgetResource(req.Resource)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(resource, stored.Resource) {
		return fixed("spec.resource", "resource")
	}
	consumers, err := normalizeBudgetConsumers(withProjectConsumer(req.Consumers, req.ProjectId))
	if err != nil {
		return err
	}
	if len(consumers) != len(stored.Consumers) || (len(stored.Consumers) > 0 && !reflect.DeepEqual(consumers, stored.Consumers)) {
		return fixed("spec.consumers", "consumers")
	}
	return nil
}

// update changes a budget's editable fields; Update and Import share it.
func (s *budgetService) update(ctx context.Context, ouID string, b *models.Budget, req budgetUpdateRequest) (*spec.Budget, error) {
	name := b.Name
	cfg := b.Configuration
	displayName, err := budgetDisplayName(req.DisplayName, name)
	if err != nil {
		return nil, err
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
	if b.Origin != models.BudgetOriginControlPlane && !gatewayFieldsUnchanged(&cfg, status, limit, exemptions) {
		// A gateway's budget: the gateway owns what it enforces; only Agent Manager's
		// fields, which gateways never get, change here (Interfaces 4.3).
		return nil, &BudgetError{
			Code: utils.ErrCodeBudgetReadOnly,
			Message: fmt.Sprintf("Budget %s was created on a gateway: status, limit and exemptions can be changed only "+
				"there. displayName, description and warnings can be changed here.", name),
		}
	}
	cfg.DisplayName, cfg.Description, cfg.Status = displayName, req.Description, status
	cfg.Limit, cfg.Exemptions, cfg.Warnings = limit, exemptions, warnings
	if b.ProjectName != "" {
		if err := ownByProject(&cfg, b.ProjectName); err != nil {
			return nil, err
		}
	}
	if err := s.checkEditableReferences(ctx, ouID, &cfg); err != nil {
		return nil, err
	}
	b.Configuration = cfg
	// A gateway's budget changes only in fields gateways never get: nothing to send.
	sendToGateways := b.Origin == models.BudgetOriginControlPlane
	if err := s.budgetRepo.Update(ctx, b); err != nil {
		if errors.Is(err, utils.ErrBudgetNotFound) {
			return nil, budgetNotFound(name)
		}
		return nil, err
	}
	if sendToGateways {
		s.gateways.SendBudget(ctx, b, false)
	}
	if s.warner != nil {
		// A lower limit or a new threshold may already be crossed.
		s.warner.CheckBudget(ctx, b)
	}
	return s.response(ctx, ouID, b)
}

// gatewayFieldsUnchanged reports whether an update leaves a budget's enforced fields
// (status, limit, exemptions) as stored. A stored block limit with no blockAt blocks at 100%.
func gatewayFieldsUnchanged(stored *models.BudgetConfig, status string, limit models.BudgetLimit, exemptions []models.BudgetExemption) bool {
	storedLimit := stored.Limit
	if storedLimit.Action == budgetActionBlock && storedLimit.BlockAt == nil {
		blockAt := int32(budgetDefaultBlockAt)
		storedLimit.BlockAt = &blockAt
	}
	storedStatus := stored.Status
	if storedStatus == "" {
		storedStatus = budgetStatusActive
	}
	return storedStatus == status && reflect.DeepEqual(storedLimit, limit) &&
		len(stored.Exemptions) == len(exemptions) && (len(exemptions) == 0 || reflect.DeepEqual(stored.Exemptions, exemptions))
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
	s.gateways.SendBudgetDeleted(ctx, b)
	audit.Record(
		ctx, audit.ActionBudgetDelete,
		audit.Org(ouID),
		audit.ResourceNamed("budget", b.ID.String(), name),
		audit.Detail("budgetName", name),
	)
	return nil
}

// budgetFilterTypes are the types a list filter can name.
var budgetFilterTypes = []string{budgetTypeProject, budgetTypeAgent, budgetTypeAPIKey, budgetTypeLLMProvider}

type budgetFilter struct{ typ, id string }

// parseBudgetFilters reads type:id filters (Interfaces 2.1).
func parseBudgetFilters(filters []string) ([]budgetFilter, error) {
	out := make([]budgetFilter, 0, len(filters))
	for i, f := range filters {
		typ, id, ok := strings.Cut(f, ":")
		if !ok || id == "" || !containsString(budgetFilterTypes, typ) {
			return nil, invalidBudget(fmt.Sprintf("filter[%d]", i),
				"filter must be type:id, with type project, agent, apiKey or llmProvider.")
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

// budgetDisplayName is the display name given, or the budget's name when none is
// (Interfaces 2.2).
func budgetDisplayName(in *string, name string) (string, error) {
	if in == nil || strings.TrimSpace(*in) == "" {
		return name, nil
	}
	displayName := strings.TrimSpace(*in)
	if len([]rune(displayName)) > maxBudgetDisplayName {
		return "", invalidBudget("displayName", "displayName must be at most %d characters.", maxBudgetDisplayName)
	}
	return displayName, nil
}

// normalizeBudget checks the payload (Interfaces 2.2) and returns the budget's settings
// with defaults filled in.
func normalizeBudget(req budgetRequest) (*models.BudgetConfig, error) {
	if !budgetNamePattern.MatchString(req.Name) {
		return nil, invalidBudget("name", "name must be 1-63 lowercase letters, digits or '-', starting and ending with a letter or digit.")
	}
	displayName, err := budgetDisplayName(req.DisplayName, req.Name)
	if err != nil {
		return nil, err
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
	status := budgetStatusActive
	if req.Status != nil {
		if *req.Status != budgetStatusActive && *req.Status != budgetStatusInactive {
			return nil, invalidBudget("status", "status must be active or inactive.")
		}
		status = *req.Status
	}
	return &models.BudgetConfig{
		DisplayName: displayName, Description: req.Description, Status: status,
		Limit: limit, Resource: resource,
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
			return nil, invalidBudget(field+".type", "Consumer type must be project, agent, apiKey or attribute.")
		}
		if !containsString(gatewayConsumerTypes, c.Type) {
			return nil, unsupportedConsumer(field+".type", c.Type)
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
		if containsString([]string{budgetTypeProject, budgetTypeAgent, budgetTypeAttribute}, e.Type) &&
			!containsString(gatewayConsumerTypes, e.Type) {
			return nil, unsupportedConsumer(field+".type", e.Type)
		}
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
	if len(in.Channels) < 1 || len(in.Channels) > maxBudgetChannels {
		return nil, invalidBudget("warnings.channels", "warnings.channels takes 1-%d channels.", maxBudgetChannels)
	}
	// Channels are named, and stored by name as received (the kind as received).
	channels := make([]string, 0, len(in.Channels))
	seenChannel := map[string]bool{}
	for i, raw := range in.Channels {
		name := strings.TrimSpace(raw)
		if name == "" || seenChannel[name] {
			return nil, invalidBudget(fmt.Sprintf("warnings.channels[%d]", i), "Channel names must be non-empty and unique.")
		}
		seenChannel[name] = true
		channels = append(channels, name)
	}
	return &models.BudgetWarnings{Thresholds: in.Thresholds, Channels: channels}, nil
}

// checkReferences checks that the providers, projects, agents, LLM proxies, environments
// and channels the budget names exist in the organization.
func (s *budgetService) checkReferences(ctx context.Context, ouID string, b *models.BudgetConfig) error {
	if b.Resource != nil && b.Resource.ID != nil {
		if err := s.checkArtifact(ouID, models.KindLLMProvider, *b.Resource.ID, "resource.id", "LLM provider"); err != nil {
			return err
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
		return s.checkChannels(ctx, ouID, budgetProject(b), b.Warnings)
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

// checkChannels checks each warning channel, given by name, exists and is available to
// the budget (Interfaces 2.4): availability all or org, or projects when the budget's
// project consumer is one of them.
func (s *budgetService) checkChannels(ctx context.Context, ouID, project string, warnings *models.BudgetWarnings) error {
	destinations, err := s.eventRepo.ListDestinations(ctx, ouID)
	if err != nil {
		return fmt.Errorf("failed to list notification channels: %w", err)
	}
	byName := make(map[string]models.EventDestination, len(destinations))
	for _, d := range destinations {
		byName[d.Name] = d
	}
	for i, name := range warnings.Channels {
		field := fmt.Sprintf("warnings.channels[%d]", i)
		destination, ok := byName[name]
		if !ok {
			return &BudgetError{
				Code: utils.ErrCodeChannelNotAvailable, Field: field,
				Message: fmt.Sprintf("Notification channel %s does not exist.", name),
			}
		}
		if destination.OUID != ouID || !destination.AvailableToBudget(project) {
			return &BudgetError{
				Code: utils.ErrCodeChannelNotAvailable, Field: field,
				Message: fmt.Sprintf("Notification channel %s is not available to this budget.", name),
			}
		}
	}
	return nil
}

// BudgetResponse is a stored budget as the API returns it (Interfaces 2.3).
// BudgetAsKind is a stored budget as the API returns it: the Budget kind, with the
// project-id annotation for a project budget and the server's fields in status.
func BudgetAsKind(b *models.Budget) *spec.Budget {
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
	displayName, status := c.DisplayName, c.Status
	budgetSpec := spec.BudgetSpec{
		DisplayName: &displayName, Description: c.Description, Status: &status,
		Limit:     spec.BudgetLimit{Amount: c.Limit.Amount, Currency: &currency, Period: c.Limit.Period, Action: c.Limit.Action, BlockAt: c.Limit.BlockAt},
		Consumers: consumers, Exemptions: exemptions,
	}
	if c.Resource != nil {
		allocation := c.Resource.Allocation
		budgetSpec.Resource = &spec.BudgetResource{Type: c.Resource.Type, Allocation: &allocation, Id: c.Resource.ID}
	}
	if c.Warnings != nil {
		budgetSpec.Warnings = &spec.BudgetWarnings{Thresholds: c.Warnings.Thresholds, Channels: c.Warnings.Channels}
	}
	metadata := spec.BudgetMetadata{Name: b.Name}
	if b.ProjectName != "" {
		metadata.Annotations = &map[string]string{AnnotationProjectID: b.ProjectName}
	}
	serverStatus := spec.NewBudgetServerStatus(string(b.Origin), b.CreatedAt, b.UpdatedAt)
	if b.CreatedBy != "" {
		serverStatus.CreatedBy = &b.CreatedBy
	}
	kind := spec.NewBudget(budgetAPIVersion, models.KindBudget, metadata, budgetSpec)
	kind.Status = serverStatus
	return kind
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
