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
  SearchBar,
  Stack,
  Typography,
} from "@wso2/oxygen-ui";
import { Plus, Wallet } from "@wso2/oxygen-ui-icons-react";
import { useFeatureManagement } from "@agent-management-platform/feature-management";
import { useListBudgets } from "@agent-management-platform/api-client";
import {
  isListEmpty,
  ResourceListShell,
} from "@agent-management-platform/shared-component";
import {
  BUDGET_PROJECT_ANNOTATION,
  type Budget,
} from "@agent-management-platform/types";
import { amount, formatUsd } from "../costFormat";
import { PERIOD_LABELS } from "./budgetLabels";

interface BudgetsTabProps {
  orgName?: string;
  /** Set on a project's page: only the budgets that project owns. */
  projectName?: string;
  /** Opens a budget's detail page. */
  onOpen: (budgetName: string) => void;
  /** Opens the create page. */
  onCreate: () => void;
}

function ownerOf(b: Budget): string {
  return b.metadata.annotations?.[BUDGET_PROJECT_ANNOTATION] ?? "Organization";
}

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
  const share = limit > 0 ? Math.round((spent / limit) * 100) : undefined;
  return (
    <Stack spacing={0}>
      <Typography variant="body2">{formatUsd(spent)}</Typography>
      <Typography
        variant="caption"
        color={
          share !== undefined && share >= 100 ? "error.main" : "text.secondary"
        }
      >
        {share !== undefined ? `${share}% of the limit` : ""}
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
  const canWrite = useFeatureManagement().hasAccess("budget_create");
  const { data, isLoading, error } = useListBudgets(
    { orgName },
    { projectId: projectName, limit: 100 },
  );
  const [search, setSearch] = useState("");
  const budgets = useMemo(() => data?.budgets ?? [], [data]);
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return budgets;
    return budgets.filter(
      (b) =>
        b.metadata.name.toLowerCase().includes(q) ||
        (b.spec.displayName ?? "").toLowerCase().includes(q),
    );
  }, [budgets, search]);
  const isFiltered = !!search.trim();

  return (
    <ResourceListShell
      resourceName="budgets"
      disablePaper
      searchValue={search}
      onSearchChange={setSearch}
      searchPlaceholder="Search budgets"
      toolbar={
        canWrite ? (
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
            <Button
              variant="contained"
              startIcon={<Plus size={16} />}
              onClick={onCreate}
              sx={{ whiteSpace: "nowrap", flexShrink: 0 }}
            >
              Create budget
            </Button>
          </Stack>
        ) : undefined
      }
      error={
        error
          ? new Error("Couldn't load budgets. Reload the page to try again.")
          : undefined
      }
      isLoading={isLoading}
      isEmpty={isListEmpty({ isLoading, count: budgets.length, isFiltered })}
      isSearchEmpty={!isLoading && budgets.length > 0 && filtered.length === 0}
      emptyState={{
        illustration: <Wallet size={64} />,
        title: "No budgets yet",
        description: projectName
          ? "Budgets this project owns appear here, with what they spent this period."
          : "Budgets cap what LLM traffic may spend per day, week or month, and warn before they run out.",
        // The toolbar, with its Create button, is not shown while the list is empty.
        action: canWrite ? (
          <Button
            variant="contained"
            startIcon={<Plus size={16} />}
            onClick={onCreate}
          >
            Create budget
          </Button>
        ) : undefined,
      }}
      searchEmptyState={{
        illustration: <Wallet size={64} />,
        title: "No budgets match your search",
        description: "Try other search keywords.",
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
                sx={{ cursor: "pointer" }}
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
              </ListingTable.Row>
            );
          })}
        </ListingTable.Body>
      </ListingTable>
    </ResourceListShell>
  );
}
