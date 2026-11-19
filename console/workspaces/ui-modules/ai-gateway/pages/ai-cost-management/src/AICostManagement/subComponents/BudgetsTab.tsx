/**
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import { useMemo, useState } from "react";
import {
  Button,
  Chip,
  ListingTable,
  ListItemIcon,
  ListItemText,
  Menu,
  MenuItem,
  SearchBar,
  Stack,
  TextField,
  Typography,
} from "@wso2/oxygen-ui";
import {
  ChevronDown,
  Plus,
  Trash2,
  Upload,
  Wallet,
} from "@wso2/oxygen-ui-icons-react";
import { useFeatureManagement } from "@agent-management-platform/feature-management";
import { useListBudgets } from "@agent-management-platform/api-client";
import {
  isListEmpty,
  ResourceListShell,
} from "@agent-management-platform/shared-component";
import { useConfirmDeleteBudget } from "./useConfirmDeleteBudget";
import {
  BUDGET_PROJECT_ANNOTATION,
  type Budget,
} from "@agent-management-platform/types";
import { amount, formatUsd } from "../costFormat";
import { PERIOD_LABELS } from "./budgetLabels";
import { useEntityNames } from "../useEntityNames";
import { ImportBudgetsDialog } from "./ImportBudgetsDialog";

/** Delete shows on hover or focus, as in the console's other lists; touch shows it always. */
const ROW_HOVER_SX = {
  "& .budget-actions": { visibility: "hidden" },
  "&:hover .budget-actions, &:focus-within .budget-actions": {
    visibility: "visible",
  },
  "@media (hover: none)": { "& .budget-actions": { visibility: "visible" } },
} as const;

interface BudgetsTabProps {
  orgName?: string;
  /** Set on a project's page: only the budgets that project owns. */
  projectName?: string;
  /** Opens a budget's detail page. */
  onOpen: (budgetName: string) => void;
  /** Opens the create page. */
  onCreate: () => void;
}

/** The kind a budget counts separately (each agent, each API key, ...), if any. */
export function separateKind(b: Budget): string | undefined {
  const kinds: string[] = [];
  if (b.spec.resource?.allocation === "separate") kinds.push("LLM provider");
  for (const c of b.spec.consumers ?? []) {
    if (c.allocation !== "separate") continue;
    kinds.push(
      c.type === "apiKey"
        ? "API key"
        : c.type === "attribute"
          ? "value"
          : c.type,
    );
  }
  if (!kinds.length) return undefined;
  return kinds.length === 1 ? kinds[0] : `${kinds.join(" and ")} combination`;
}

/** Whether the project is one of the budget's consumers, itself or through an agent. */
export function consumes(b: Budget, project: string): boolean {
  return (b.spec.consumers ?? []).some(
    (c) =>
      (c.type === "project" && c.id === project) ||
      (c.type === "agent" && c.project === project),
  );
}

/** Spend at or past the point the budget acts: its block point, or the amount. */
export function atOrOverLimit(b: Budget): boolean {
  const spent = amount(b.status?.currentPeriod?.spent);
  const limit = amount(b.spec.limit.amount);
  const point =
    b.spec.limit.action === "block"
      ? (limit * (b.spec.limit.blockAt ?? 100)) / 100
      : limit;
  return limit > 0 && !separateKind(b) && spent >= point;
}

type StatusFilter = "all" | "active" | "inactive" | "over";
const STATUS_FILTERS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "All budgets" },
  { value: "active", label: "Active" },
  { value: "inactive", label: "Inactive" },
  { value: "over", label: "Limit reached" },
];

/** This period's spend and its share of the limit. */
function Usage({ budget }: { budget: Budget }) {
  const period = budget.status?.currentPeriod;
  if (!period) {
    return (
      <Typography variant="body2" color="text.secondary">
        No spend yet
      </Typography>
    );
  }
  const spent = amount(period.spent);
  const limit = amount(budget.spec.limit.amount);
  // Counted separately, each allowance has the whole limit: the total is not a share of it.
  const each = separateKind(budget);
  const share =
    limit > 0 && !each ? Math.round((spent / limit) * 100) : undefined;
  const inactive = budget.spec.status === "inactive";
  return (
    <Stack spacing={0}>
      <Typography variant="body2">{formatUsd(spent)}</Typography>
      <Typography
        variant="caption"
        color={
          !inactive && share !== undefined && share >= 100
            ? "error.main"
            : "text.secondary"
        }
      >
        {each
          ? `Each ${each} has its own limit`
          : share !== undefined
            ? `${share}% of the limit`
            : ""}
        {period.unpricedRequests > 0
          ? ` · ${period.unpricedRequests} unpriced`
          : ""}
      </Typography>
    </Stack>
  );
}

/** Every budget of the organization, or of one project, with its spend this period. */
export function BudgetsTab({
  orgName,
  projectName,
  onOpen,
  onCreate,
}: BudgetsTabProps) {
  // Budgets are managed at organization level; a project sees, read-only, the budgets
  // it is a consumer of.
  const { hasAccess } = useFeatureManagement();
  const canWrite = hasAccess("budget_create") && !projectName;
  const canDelete = hasAccess("budget_delete") && !projectName;
  const { confirmDelete } = useConfirmDeleteBudget(orgName);
  const { data, isLoading, error } = useListBudgets(
    { orgName },
    {
      filter: projectName ? `project:${projectName}` : undefined,
      limit: 100,
    },
  );
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [importing, setImporting] = useState(false);
  const names = useEntityNames(orgName ?? "");
  const ownerOf = (b: Budget) => {
    const project = b.metadata.annotations?.[BUDGET_PROJECT_ANNOTATION];
    return project ? (names.project?.[project] ?? project) : "Organization";
  };
  // Ordered by the name people see, not the handle.
  const budgets = useMemo(
    () =>
      [...(data?.budgets ?? [])]
        .filter((b) => !projectName || consumes(b, projectName))
        .sort((a, b) =>
          (a.spec.displayName ?? a.metadata.name).localeCompare(
            b.spec.displayName ?? b.metadata.name,
            undefined,
            { sensitivity: "base" },
          ),
        ),
    [data, projectName],
  );
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return budgets.filter((b) => {
      const inactive = b.spec.status === "inactive";
      if (statusFilter === "active" && inactive) return false;
      if (statusFilter === "inactive" && !inactive) return false;
      if (statusFilter === "over" && (inactive || !atOrOverLimit(b)))
        return false;
      return (
        !q ||
        b.metadata.name.toLowerCase().includes(q) ||
        (b.spec.displayName ?? "").toLowerCase().includes(q)
      );
    });
  }, [budgets, search, statusFilter]);
  const isFiltered = !!search.trim() || statusFilter !== "all";
  const statusSelect = (
    <TextField
      select
      size="small"
      value={statusFilter}
      onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
      sx={{ minWidth: 200, flexShrink: 0 }}
      slotProps={{ htmlInput: { "aria-label": "Filter by status" } }}
    >
      {STATUS_FILTERS.map((f) => (
        <MenuItem key={f.value} value={f.value}>
          {f.label}
        </MenuItem>
      ))}
    </TextField>
  );

  return (
    <>
      <ResourceListShell
        resourceName="budgets"
        disablePaper
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="Search budgets"
        toolbar={
          <Stack
            direction={{ xs: "column", md: "row" }}
            spacing={1.5}
            alignItems={{ xs: "stretch", md: "center" }}
          >
            <SearchBar
              placeholder="Search budgets"
              size="small"
              fullWidth
              value={search}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                setSearch(e.target.value)
              }
            />
            {statusSelect}
            {canWrite && (
              <CreateBudgetMenu
                onCreate={onCreate}
                onImport={() => setImporting(true)}
              />
            )}
          </Stack>
        }
        error={
          error
            ? new Error("Couldn't load budgets. Reload the page to try again.")
            : undefined
        }
        isLoading={isLoading}
        isEmpty={isListEmpty({ isLoading, count: budgets.length, isFiltered })}
        isSearchEmpty={
          !isLoading && budgets.length > 0 && filtered.length === 0
        }
        emptyState={{
          illustration: <Wallet size={64} />,
          title: projectName
            ? "No budgets apply to this project"
            : "No budgets yet",
          description: projectName
            ? "Budgets that name this project or its agents appear here."
            : "Budgets cap what LLM traffic may spend per day, week or month, and warn before they run out.",
          // The toolbar, with its Create button, is not shown while the list is empty.
          action: canWrite ? (
            <CreateBudgetMenu
              onCreate={onCreate}
              onImport={() => setImporting(true)}
            />
          ) : undefined,
        }}
        searchEmptyState={{
          illustration: <Wallet size={64} />,
          title: "No budgets match",
          description: "Try other search keywords or another filter.",
        }}
      >
        <ListingTable>
          <ListingTable.Head>
            <ListingTable.Row>
              <ListingTable.Cell>Budget</ListingTable.Cell>
              {!projectName && <ListingTable.Cell>Owner</ListingTable.Cell>}
              <ListingTable.Cell>Amount (USD)</ListingTable.Cell>
              <ListingTable.Cell>Period</ListingTable.Cell>
              <ListingTable.Cell>At the limit</ListingTable.Cell>
              <ListingTable.Cell>This period</ListingTable.Cell>
              <ListingTable.Cell>Warnings</ListingTable.Cell>
              <ListingTable.Cell>Status</ListingTable.Cell>
              {canDelete && <ListingTable.Cell />}
            </ListingTable.Row>
          </ListingTable.Head>
          <ListingTable.Body>
            {filtered.map((b) => {
              const { limit, warnings, status, displayName } = b.spec;
              const inactive = status === "inactive";
              return (
                <ListingTable.Row
                  key={b.metadata.name}
                  hover
                  onClick={() => onOpen(b.metadata.name)}
                  // Inactive budgets count and block nothing: shown, but in the background.
                  sx={{
                    cursor: "pointer",
                    opacity: inactive ? 0.6 : 1,
                    ...ROW_HOVER_SX,
                  }}
                >
                  <ListingTable.Cell>
                    <Stack spacing={0}>
                      <Typography variant="body2" fontWeight={600}>
                        {displayName ?? b.metadata.name}
                      </Typography>
                      {displayName && displayName !== b.metadata.name && (
                        <Typography variant="caption" color="text.secondary">
                          {b.metadata.name}
                        </Typography>
                      )}
                    </Stack>
                  </ListingTable.Cell>
                  {!projectName && (
                    <ListingTable.Cell>{ownerOf(b)}</ListingTable.Cell>
                  )}
                  <ListingTable.Cell>
                    {formatUsd(amount(limit.amount))}
                  </ListingTable.Cell>
                  <ListingTable.Cell>
                    {PERIOD_LABELS[limit.period]}
                  </ListingTable.Cell>
                  <ListingTable.Cell>
                    <Chip
                      size="small"
                      variant="outlined"
                      color={limit.action === "block" ? "warning" : "default"}
                      label={limit.action === "block" ? "Block" : "Allow"}
                    />
                  </ListingTable.Cell>
                  <ListingTable.Cell>
                    <Usage budget={b} />
                  </ListingTable.Cell>
                  <ListingTable.Cell>
                    {warnings?.thresholds.length
                      ? warnings.thresholds.map((t) => `${t}%`).join(", ")
                      : "None"}
                  </ListingTable.Cell>
                  <ListingTable.Cell>
                    <Chip
                      size="small"
                      color={inactive ? "default" : "success"}
                      variant="outlined"
                      label={inactive ? "Inactive" : "Active"}
                    />
                  </ListingTable.Cell>
                  {canDelete && (
                    <ListingTable.Cell align="right" sx={{ width: 110 }}>
                      {/* Gateway-made budgets are deleted on their gateway. */}
                      {b.status?.origin !== "gateway_api" && (
                        <Stack className="budget-actions" direction="row">
                          <Button
                            variant="outlined"
                            color="error"
                            size="small"
                            startIcon={<Trash2 size={14} />}
                            onClick={(e) => {
                              e.stopPropagation();
                              confirmDelete(
                                b.metadata.name,
                                displayName ?? b.metadata.name,
                              );
                            }}
                          >
                            Delete
                          </Button>
                        </Stack>
                      )}
                    </ListingTable.Cell>
                  )}
                </ListingTable.Row>
              );
            })}
          </ListingTable.Body>
        </ListingTable>
      </ResourceListShell>
      <ImportBudgetsDialog
        open={importing}
        orgName={orgName}
        onClose={() => setImporting(false)}
      />
    </>
  );
}

/** Create budget, with a menu right under it: the form, or an import from a CSV file. */
function CreateBudgetMenu({
  onCreate,
  onImport,
}: {
  onCreate: () => void;
  onImport: () => void;
}) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const pick = (then: () => void) => () => {
    setAnchor(null);
    then();
  };
  return (
    <>
      <Button
        variant="contained"
        startIcon={<Plus size={16} />}
        endIcon={<ChevronDown size={16} />}
        onClick={(e) => setAnchor(e.currentTarget)}
        aria-haspopup="menu"
        aria-expanded={anchor ? "true" : undefined}
        sx={{ whiteSpace: "nowrap", flexShrink: 0 }}
      >
        Create budget
      </Button>
      <Menu
        open={!!anchor}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
        transformOrigin={{ vertical: "top", horizontal: "right" }}
      >
        <MenuItem onClick={pick(onCreate)}>
          <ListItemIcon>
            <Plus size={16} />
          </ListItemIcon>
          <ListItemText
            primary="Create manually"
            secondary="Configure a new budget"
          />
        </MenuItem>
        <MenuItem onClick={pick(onImport)}>
          <ListItemIcon>
            <Upload size={16} />
          </ListItemIcon>
          <ListItemText
            primary="Import from CSV"
            secondary="Create or update budgets in bulk"
          />
        </MenuItem>
      </Menu>
    </>
  );
}
