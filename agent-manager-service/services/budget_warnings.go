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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wso2/agent-manager/agent-manager-service/events"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// BudgetWarningEvent is the warning's event type (Interfaces 6).
const BudgetWarningEvent = "budget.threshold_crossed"

// GatewayUsageMessage is the usage message a gateway sends on its connection
// (Interfaces 4.2): budget allowance totals.
type GatewayUsageMessage struct {
	Allowances []GatewayAllowanceUsage `json:"allowances"`
}

// GatewayAllowanceUsage is one allowance's total in one period, across every gateway
// sharing the spend tracker.
type GatewayAllowanceUsage struct {
	Budget           string            `json:"budget"`
	PeriodStart      string            `json:"periodStart"`
	AllowanceUUID    string            `json:"allowanceUuid"`
	Allowance        map[string]string `json:"allowance"`
	Spent            string            `json:"spent"`
	UnpricedRequests int64             `json:"unpricedRequests"`
}

// BudgetWarner checks a budget's thresholds against its stored spend; run when a
// budget is edited, since a lower limit or a new threshold may already be crossed.
type BudgetWarner interface {
	CheckBudget(ctx context.Context, b *models.Budget)
}

// BudgetWarningService keeps the spend gateways report for each budget allowance and
// sends a warning, once, for every threshold an allowance crosses (Interfaces 1.2, 6):
// straight to the budget's notification channels, through the event dispatcher.
type BudgetWarningService struct {
	budgets  repositories.BudgetRepository
	usage    repositories.BudgetUsageRepository
	events   repositories.EventRepository
	gateways repositories.GatewayRepository
	bus      events.Bus
}

// NewBudgetWarningService creates a BudgetWarningService. bus is nil when events are off
// (EVENTS_MODE): spend is still recorded, and no warning is queued, since nothing could
// send it.
func NewBudgetWarningService(
	budgets repositories.BudgetRepository,
	usage repositories.BudgetUsageRepository,
	eventRepo repositories.EventRepository,
	gateways repositories.GatewayRepository,
	bus events.Bus,
) *BudgetWarningService {
	return &BudgetWarningService{budgets: budgets, usage: usage, events: eventRepo, gateways: gateways, bus: bus}
}

// HandleUsage records a gateway's usage message. An allowance whose budget is unknown or
// whose fields are invalid is skipped and logged; the gateway sends totals again.
func (s *BudgetWarningService) HandleUsage(ctx context.Context, gatewayID string, payload json.RawMessage) {
	log := slog.With("gatewayID", gatewayID)
	var msg GatewayUsageMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Error("Failed to parse budget usage", "error", err)
		return
	}
	gateway, err := s.gateways.GetByUUID(gatewayID)
	if err != nil || gateway == nil {
		log.Error("Failed to find the gateway of a budget usage report", "error", err)
		return
	}
	ouID := gateway.OUID
	budgets := map[string]*models.Budget{}
	for _, a := range msg.Allowances {
		b, ok := budgets[a.Budget]
		if !ok {
			b, err = s.budgets.Get(ctx, ouID, a.Budget)
			if errors.Is(err, utils.ErrBudgetNotFound) {
				b = nil
			} else if err != nil {
				log.Error("Failed to load a budget for its usage", "budget", a.Budget, "error", err)
				return
			}
			budgets[a.Budget] = b
		}
		if b == nil {
			log.Debug("Usage for an unknown budget ignored", "budget", a.Budget)
			continue
		}
		usage, err := usageRow(b, ouID, a)
		if err != nil {
			log.Warn("Invalid budget usage ignored", "budget", a.Budget, "error", err)
			continue
		}
		s.record(ctx, b, usage, log)
	}
}

// usageRow validates one reported allowance.
func usageRow(b *models.Budget, ouID string, a GatewayAllowanceUsage) (models.BudgetUsage, error) {
	allowanceID, err := uuid.Parse(a.AllowanceUUID)
	if err != nil {
		return models.BudgetUsage{}, fmt.Errorf("allowanceUuid: %w", err)
	}
	periodStart, err := time.Parse(time.DateOnly, a.PeriodStart)
	if err != nil {
		return models.BudgetUsage{}, fmt.Errorf("periodStart: %w", err)
	}
	spent, ok := new(big.Rat).SetString(a.Spent)
	if !ok || spent.Sign() < 0 || a.UnpricedRequests < 0 {
		return models.BudgetUsage{}, fmt.Errorf("spent %q or unpricedRequests %d", a.Spent, a.UnpricedRequests)
	}
	return models.BudgetUsage{
		BudgetID: b.ID, AllowanceID: allowanceID, OUID: ouID,
		Allowance: allowanceValues(a.Allowance), PeriodStart: periodStart,
		Spent: a.Spent, UnpricedRequests: a.UnpricedRequests,
	}, nil
}

// allowanceValues are the values an allowance is separated by, as pages and warnings
// show them: an agent ("project/agent" on the gateway) is split into project and agent.
func allowanceValues(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k == budgetTypeAgent {
			if project, agent, ok := strings.Cut(v, "/"); ok {
				out[budgetTypeProject], out[budgetTypeAgent] = project, agent
				continue
			}
		}
		out[k] = v
	}
	return out
}

// record stores one allowance's total and queues the warnings it crosses.
func (s *BudgetWarningService) record(ctx context.Context, b *models.Budget, usage models.BudgetUsage, log *slog.Logger) {
	channels, err := s.channels(ctx, b)
	if err != nil {
		log.Error("Failed to load a budget's notification channels", "budget", b.Name, "error", err)
		return
	}
	payloads := map[string][]byte{}
	var warn func(models.BudgetUsage) []models.EventDelivery
	if s.bus != nil {
		warn = func(stored models.BudgetUsage) []models.EventDelivery {
			return warningDeliveries(b, stored, channels, payloads)
		}
	}
	queued, err := s.usage.Record(ctx, usage, warn)
	if err != nil {
		log.Error("Failed to record budget usage", "budget", b.Name, "error", err)
		return
	}
	s.enqueue(ctx, queued, payloads, log)
}

// CheckBudget queues the warnings a budget's stored spend already crosses: after an
// edit lowers the limit or adds a threshold. Raising the limit gives new amounts, so
// warnings re-arm (BR-15).
func (s *BudgetWarningService) CheckBudget(ctx context.Context, b *models.Budget) {
	log := slog.With("budget", b.Name)
	if s.bus == nil || b.Configuration.Warnings == nil || len(b.Configuration.Warnings.Thresholds) == 0 {
		return
	}
	rows, err := s.usage.ForBudget(ctx, b.ID)
	if err != nil {
		log.Error("Failed to load a budget's spend", "error", err)
		return
	}
	channels, err := s.channels(ctx, b)
	if err != nil {
		log.Error("Failed to load a budget's notification channels", "error", err)
		return
	}
	payloads := map[string][]byte{}
	var deliveries []models.EventDelivery
	for _, row := range rows {
		deliveries = append(deliveries, warningDeliveries(b, row, channels, payloads)...)
	}
	if len(deliveries) == 0 {
		return
	}
	queued, err := s.usage.QueueWarnings(ctx, deliveries)
	if err != nil {
		log.Error("Failed to queue budget warnings", "error", err)
		return
	}
	s.enqueue(ctx, queued, payloads, log)
}

// channels returns the budget's notification channels it may still send to.
func (s *BudgetWarningService) channels(ctx context.Context, b *models.Budget) ([]models.EventDestination, error) {
	w := b.Configuration.Warnings
	if s.bus == nil || w == nil || len(w.Thresholds) == 0 || len(w.ChannelIDs) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(w.ChannelIDs))
	for _, raw := range w.ChannelIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	found, err := s.events.DestinationsByID(ctx, b.OrgID, ids)
	if err != nil {
		return nil, err
	}
	project := budgetProject(&b.Configuration)
	out := found[:0]
	for _, d := range found {
		if d.AvailableToBudget(project) {
			out = append(out, d)
		}
	}
	return out, nil
}

// enqueue hands queued deliveries to the event dispatcher. A delivery that cannot be
// queued stays pending and is failed when stale.
func (s *BudgetWarningService) enqueue(ctx context.Context, queued []models.EventDelivery, payloads map[string][]byte, log *slog.Logger) {
	for _, d := range queued {
		body, err := json.Marshal(eventDeliveryJob{DestinationID: d.DestinationID.String(), Event: payloads[d.EventID]})
		if err != nil {
			log.Error("Failed to encode a budget warning", "event", d.EventID, "error", err)
			continue
		}
		if err := s.bus.Enqueue(ctx, events.QueueEventDeliveries, d.EventID+":"+d.DestinationID.String(), body); err != nil {
			log.Error("Failed to queue a budget warning", "event", d.EventID, "destinationId", d.DestinationID, "error", err)
		}
	}
}

// warningDeliveries returns a delivery to every channel for every threshold the stored
// spend has reached, with its event in payloads. A warning's ID is the budget, period,
// allowance and threshold amount, so each is sent once per channel (BR-13).
func warningDeliveries(b *models.Budget, row models.BudgetUsage, channels []models.EventDestination, payloads map[string][]byte) []models.EventDelivery {
	w := b.Configuration.Warnings
	if w == nil || len(channels) == 0 || b.Configuration.Status != budgetStatusActive {
		return nil
	}
	amount, ok := new(big.Rat).SetString(b.Configuration.Limit.Amount)
	if !ok || amount.Sign() <= 0 {
		return nil
	}
	spent, ok := new(big.Rat).SetString(row.Spent)
	if !ok {
		return nil
	}
	var out []models.EventDelivery
	for _, t := range w.Thresholds {
		at := new(big.Rat).Mul(amount, big.NewRat(int64(t), 100))
		if spent.Cmp(at) < 0 {
			continue
		}
		e := warningEvent(b, row, int(t), at, spent)
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		payloads[e.ID] = data
		for _, ch := range channels {
			out = append(out, models.EventDelivery{
				BudgetID: &b.ID, OUID: b.OrgID, DestinationID: ch.ID, SubscriptionIDs: []string{},
				EventID: e.ID, EventType: e.Name(), EventScope: string(e.Scope),
				ProjectName: e.Project, AgentName: e.Agent, Status: models.EventDeliveryPending,
			})
		}
	}
	return out
}

// warningEvent is the CloudEvent of one threshold crossed (Interfaces 6).
func warningEvent(b *models.Budget, row models.BudgetUsage, threshold int, at, spent *big.Rat) events.Event {
	period := row.PeriodStart.UTC()
	id := fmt.Sprintf("budget:%s:%s:%s:%s", b.ID, period.Format(time.DateOnly), row.AllowanceID, at.FloatString(2))
	project := row.Allowance[budgetTypeProject]
	if project == "" {
		project = budgetProject(&b.Configuration)
	}
	agent := row.Allowance[budgetTypeAgent]
	scope := events.ScopeOrg
	switch {
	case project != "" && agent != "":
		scope = events.ScopeAgent
	case project != "":
		scope, agent = events.ScopeProject, ""
	default:
		agent = ""
	}
	limit := b.Configuration.Limit
	resets := periodEnd(limit.Period, period)
	displayName := b.Configuration.DisplayName
	if displayName == "" {
		displayName = b.Name
	}
	e := events.New(id, BudgetWarningEvent, scope, b.OrgID, project, agent, time.Now())
	e.Data = map[string]any{
		"budget":      b.Name,
		"displayName": displayName,
		"allowance":   row.Allowance,
		"limit":       limit.Amount,
		"action":      limit.Action,
		"threshold":   threshold,
		"spent":       spent.FloatString(2),
		"resetsAt":    resets.Format(time.RFC3339),
		"message": fmt.Sprintf("%s at %d%% ($%s of $%s), resets %s", allowanceLabel(row.Allowance), threshold,
			spent.FloatString(2), amountString(limit.Amount), resets.Format("2 Jan")),
	}
	return e
}

// allowanceLabel names an allowance in a warning: its values, or "Spend" when the
// budget is shared.
func allowanceLabel(values map[string]string) string {
	if len(values) == 0 {
		return "Spend"
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == budgetTypeProject && values[budgetTypeAgent] != "" {
			continue // the agent names it
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = values[k]
	}
	return strings.Join(parts, ", ")
}

func amountString(amount string) string {
	if r, ok := new(big.Rat).SetString(amount); ok {
		return r.FloatString(2)
	}
	return amount
}

// periodEnd is when a budget period starting at start resets (UTC calendar; weeks
// start Monday).
func periodEnd(period string, start time.Time) time.Time {
	switch period {
	case "day":
		return start.AddDate(0, 0, 1)
	case "week":
		return start.AddDate(0, 0, 7)
	default:
		return start.AddDate(0, 1, 0)
	}
}
