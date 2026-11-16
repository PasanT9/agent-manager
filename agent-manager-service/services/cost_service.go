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
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/spec"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// AI cost stats (Interfaces 1.3, 2.6): gateways with [ai_cost_stats] on count what every
// LLM request cost and report it in their usage messages (4.2); the totals are kept per
// month and combination of project, agent, API key, user, LLM provider and model, for 3
// months.

// costMonths is how many months are kept and shown; the current one counts as one.
const costMonths = 3

// costRetentionInterval is how often months older than costMonths are deleted.
const costRetentionInterval = 24 * time.Hour

// Cost group and filter types.
const (
	costByOrganization = "organization"
	costByProject      = "project"
	costByAgent        = "agent"
	costByAPIKey       = "apiKey"
	costByUser         = "user"
	costByLLMProvider  = "llmProvider"
	costByModel        = "model"
)

var costGroupTypes = []string{costByOrganization, costByProject, costByAgent, costByAPIKey, costByUser, costByLLMProvider, costByModel}

// GatewayCostUsage is what one combination of values cost in one month, across every
// gateway sharing the counter store (Interfaces 4.2).
type GatewayCostUsage struct {
	Month                    string `json:"month"`
	CostKey                  string `json:"costKey"`
	Project                  string `json:"project"`
	Agent                    string `json:"agent"`
	APIKey                   string `json:"apiKey"`
	User                     string `json:"user"`
	LLMProvider              string `json:"llmProvider"`
	Model                    string `json:"model"`
	Cost                     string `json:"cost"`
	MissingPriceRequests     int64  `json:"missingPriceRequests"`
	UsageUnavailableRequests int64  `json:"usageUnavailableRequests"`
}

// CostRecorder stores the cost totals gateways report.
type CostRecorder interface {
	RecordCosts(ctx context.Context, ouID string, costs []GatewayCostUsage)
}

// CostService keeps and reports AI cost stats.
type CostService interface {
	CostRecorder
	// Costs returns the organization's costs over the kept months, grouped by groupBy and
	// limited by every filter (type:value[,value...]).
	Costs(ctx context.Context, ouID, groupBy string, filters []string) (*spec.CostReport, error)
	// Start deletes months older than the kept ones once a day until Stop.
	Start(ctx context.Context) error
	Stop() error
}

type costService struct {
	repo     repositories.CostUsageRepository
	now      func() time.Time
	stopCh   chan struct{}
	stopOnce sync.Once
	done     sync.WaitGroup
}

// NewCostService creates a CostService.
func NewCostService(repo repositories.CostUsageRepository) CostService {
	return &costService{repo: repo, now: time.Now, stopCh: make(chan struct{})}
}

func (s *costService) RecordCosts(ctx context.Context, ouID string, costs []GatewayCostUsage) {
	for _, c := range costs {
		row, err := costRow(ouID, c)
		if err != nil {
			slog.Warn("Ignoring an unreadable cost report", "costKey", c.CostKey, "error", err)
			continue
		}
		if row.Month.Before(costFirstMonth(s.now())) {
			continue // older than the kept months
		}
		if err := s.repo.Record(ctx, row); err != nil {
			slog.Error("Failed to record cost usage", "costKey", c.CostKey, "error", err)
		}
	}
}

// costRow maps a gateway's report onto its row.
func costRow(ouID string, c GatewayCostUsage) (models.CostUsage, error) {
	month, err := time.Parse(time.DateOnly, c.Month)
	if err != nil {
		return models.CostUsage{}, fmt.Errorf("month: %w", err)
	}
	key, err := uuid.Parse(c.CostKey)
	if err != nil {
		return models.CostUsage{}, fmt.Errorf("costKey: %w", err)
	}
	if strings.TrimSpace(c.LLMProvider) == "" {
		return models.CostUsage{}, fmt.Errorf("llmProvider is empty")
	}
	if _, ok := new(big.Rat).SetString(c.Cost); !ok {
		return models.CostUsage{}, fmt.Errorf("cost %q is not a decimal", c.Cost)
	}
	optional := func(v string) *string {
		if v = strings.TrimSpace(v); v != "" {
			return &v
		}
		return nil
	}
	return models.CostUsage{
		OUID: ouID, Month: monthOf(month), CostKey: key,
		Project: optional(c.Project), Agent: optional(c.Agent), APIKey: optional(c.APIKey), UserID: optional(c.User),
		LLMProvider: c.LLMProvider, Model: optional(c.Model), Cost: c.Cost,
		MissingPriceRequests: c.MissingPriceRequests, UsageUnavailableRequests: c.UsageUnavailableRequests,
	}, nil
}

// monthOf is the first of t's UTC month.
func monthOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// costFirstMonth is the oldest kept month: the current one and the ones before it.
func costFirstMonth(now time.Time) time.Time {
	return monthOf(now).AddDate(0, -(costMonths - 1), 0)
}

// costFilter is one filter: rows whose value of its type is any of its values.
type costFilter struct {
	typ    string
	values []string
}

func parseCostFilters(filters []string) ([]costFilter, error) {
	out := make([]costFilter, 0, len(filters))
	for _, f := range filters {
		typ, values, ok := strings.Cut(f, ":")
		if !ok || typ == costByOrganization || !slices.Contains(costGroupTypes, typ) {
			return nil, invalidBudget("filter", "filter must be type:value with type project, agent, apiKey, user, llmProvider or model.")
		}
		var vs []string
		for _, v := range strings.Split(values, ",") {
			if v = strings.TrimSpace(v); v != "" {
				vs = append(vs, v)
			}
		}
		if len(vs) == 0 {
			return nil, invalidBudget("filter", "filter %s names no value.", typ)
		}
		out = append(out, costFilter{typ: typ, values: vs})
	}
	return out, nil
}

// costValue is a row's value of a type; an agent is project/agent.
func costValue(r models.CostUsage, typ string) string {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch typ {
	case costByProject:
		return deref(r.Project)
	case costByAgent:
		if r.Agent == nil {
			return ""
		}
		return deref(r.Project) + "/" + *r.Agent
	case costByAPIKey:
		return deref(r.APIKey)
	case costByUser:
		return deref(r.UserID)
	case costByLLMProvider:
		return r.LLMProvider
	case costByModel:
		return deref(r.Model)
	}
	return ""
}

type costTotals struct {
	group       spec.CostGroup
	byMonth     []*big.Rat
	total       *big.Rat
	missing     int64
	unavailable int64
}

func (s *costService) Costs(ctx context.Context, ouID, groupBy string, filters []string) (*spec.CostReport, error) {
	if ouID == "" {
		return nil, utils.ErrOrganizationNotFound
	}
	if groupBy == "" {
		groupBy = costByOrganization
	}
	if !slices.Contains(costGroupTypes, groupBy) {
		return nil, invalidBudget("groupBy", "groupBy must be one of %s.", strings.Join(costGroupTypes, ", "))
	}
	matchers, err := parseCostFilters(filters)
	if err != nil {
		return nil, err
	}
	first := costFirstMonth(s.now())
	months := make([]string, costMonths)
	for i := range months {
		months[i] = first.AddDate(0, i, 0).Format("2006-01")
	}
	rows, err := s.repo.Since(ctx, ouID, first)
	if err != nil {
		return nil, err
	}
	groups := map[string]*costTotals{}
	for _, r := range rows {
		if !costMatches(r, matchers) {
			continue
		}
		i := (r.Month.UTC().Year()-first.Year())*12 + int(r.Month.UTC().Month()-first.Month())
		if i < 0 || i >= costMonths {
			continue
		}
		key := ""
		if groupBy != costByOrganization {
			key = costValue(r, groupBy)
		}
		g := groups[key]
		if g == nil {
			g = &costTotals{group: costGroupOf(r, groupBy), byMonth: make([]*big.Rat, costMonths), total: new(big.Rat)}
			for m := range g.byMonth {
				g.byMonth[m] = new(big.Rat)
			}
			groups[key] = g
		}
		cost, ok := new(big.Rat).SetString(r.Cost)
		if !ok {
			cost = new(big.Rat)
		}
		g.byMonth[i].Add(g.byMonth[i], cost)
		g.total.Add(g.total, cost)
		g.missing += r.MissingPriceRequests
		g.unavailable += r.UsageUnavailableRequests
	}
	ordered := make([]*costTotals, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	// Highest total first; equal totals by value, so the order is stable.
	sort.Slice(ordered, func(i, j int) bool {
		if c := ordered[i].total.Cmp(ordered[j].total); c != 0 {
			return c > 0
		}
		return costGroupName(ordered[i].group) < costGroupName(ordered[j].group)
	})
	out := make([]spec.CostGroup, 0, len(ordered))
	for _, g := range ordered {
		group := g.group
		group.Costs = make([]string, costMonths)
		for m, c := range g.byMonth {
			group.Costs[m] = c.FloatString(2)
		}
		group.Total, group.MissingPriceRequests, group.UsageUnavailableRequests = g.total.FloatString(2), g.missing, g.unavailable
		out = append(out, group)
	}
	return spec.NewCostReport(groupBy, months, out), nil
}

// costMatches reports whether a row passes every filter.
func costMatches(r models.CostUsage, filters []costFilter) bool {
	for _, f := range filters {
		if !slices.Contains(f.values, costValue(r, f.typ)) {
			return false
		}
	}
	return true
}

// costGroupOf names a group by its value: an agent also by its project.
func costGroupOf(r models.CostUsage, groupBy string) spec.CostGroup {
	var g spec.CostGroup
	set := func(dst **string, v *string) {
		if v != nil {
			value := *v
			*dst = &value
		}
	}
	switch groupBy {
	case costByProject:
		set(&g.Project, r.Project)
	case costByAgent:
		set(&g.Project, r.Project)
		set(&g.Agent, r.Agent)
	case costByAPIKey:
		set(&g.ApiKey, r.APIKey)
	case costByUser:
		set(&g.User, r.UserID)
	case costByLLMProvider:
		provider := r.LLMProvider
		g.LlmProvider = &provider
	case costByModel:
		set(&g.Model, r.Model)
	}
	return g
}

func costGroupName(g spec.CostGroup) string {
	parts := []string{}
	for _, p := range []*string{g.Project, g.Agent, g.ApiKey, g.User, g.LlmProvider, g.Model} {
		if p != nil {
			parts = append(parts, *p)
		}
	}
	return strings.Join(parts, "/")
}

func (s *costService) Start(ctx context.Context) error {
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		ticker := time.NewTicker(costRetentionInterval)
		defer ticker.Stop()
		for {
			s.deleteOld(ctx)
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// deleteOld deletes the months older than the kept ones.
func (s *costService) deleteOld(ctx context.Context) {
	deleted, err := s.repo.DeleteBefore(ctx, costFirstMonth(s.now()))
	if err != nil {
		slog.Error("Failed to delete old cost usage", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("Deleted cost usage older than the kept months", "rows", deleted)
	}
}

func (s *costService) Stop() error {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.done.Wait()
	})
	return nil
}
