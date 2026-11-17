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

import { useMemo } from "react";
import {
  Autocomplete,
  Box,
  Button,
  Card,
  CardContent,
  Grid,
  MenuItem,
  Skeleton,
  Link,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@wso2/oxygen-ui";
import { ChartColumnStacked, ExternalLink, ServerCrash } from "@wso2/oxygen-ui-icons-react";
import { useSearchParams } from "react-router-dom";
import {
  useCosts,
  useListLLMProviders,
  useListOrgAgents,
  useListProjects,
} from "@agent-management-platform/api-client";
import { StateNotice, TextInput } from "@agent-management-platform/views";
import { globalConfig, type CostGroupBy } from "@agent-management-platform/types";
import {
  amount,
  type CostNames,
  changePercent,
  dimensionsFor,
  entryMonthly,
  formatUsd,
  groupKey,
  groupLabel,
  keptMonths,
  monthLabel,
  monthlyTotals,
  unpricedTotals,
} from "../costFormat";
import { CostChart, type CostChartRow, type CostSeries } from "./CostChart";

/** Groups shown on their own in the chart; the rest are summed as "Other". */
const TOP_GROUPS = 5;

interface CostOverviewProps {
  orgName?: string;
  /** Set on a project's page: only its cost, grouped by what is below a project. */
  projectName?: string;
}

function Tile({
  label,
  value,
  caption,
  highlight,
}: {
  label: string;
  value: string;
  caption?: string;
  highlight?: boolean;
}) {
  return (
    <Card
      variant="outlined"
      sx={{ height: "100%", borderColor: highlight ? "primary.main" : "divider" }}
    >
      <CardContent>
        <Typography variant="body2" color="text.secondary">
          {label}
        </Typography>
        <Typography variant="h5" sx={{ mt: 0.5, color: highlight ? "primary.main" : "text.primary" }}>
          {value}
        </Typography>
        {caption && (
          <Typography variant="caption" color="text.secondary">
            {caption}
          </Typography>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * "Open in Moesif" when the deployment names the organization's Moesif dashboard;
 * otherwise "Detailed analytics unavailable", linking to the documentation (ACV 3.4).
 */
function AnalyticsLink() {
  const moesif = globalConfig?.moesifDashboardUrl?.trim();
  if (moesif) {
    return (
      <Button
        size="small"
        variant="text"
        href={moesif}
        target="_blank"
        rel="noopener noreferrer"
        endIcon={<ExternalLink size={14} />}
      >
        Open in Moesif
      </Button>
    );
  }
  const docs = globalConfig?.docsUrl?.trim();
  return (
    <Tooltip title="No analytics integration is configured for this deployment.">
      <Typography variant="body2" color="text.secondary">
        {docs ? (
          <Link href={docs} target="_blank" rel="noopener noreferrer" color="inherit">
            Detailed analytics unavailable
          </Link>
        ) : (
          "Detailed analytics unavailable"
        )}
      </Typography>
    </Tooltip>
  );
}

/** What LLM traffic cost in the kept months, grouped and picked by one dimension. */
export function CostOverview({ orgName, projectName }: CostOverviewProps) {
  const projectScoped = !!projectName;
  const dimensions = dimensionsFor(projectScoped);
  // Group by and the picks live in the URL, so a cost panel can link here pre-filtered.
  const [searchParams, setSearchParams] = useSearchParams();
  const requested = searchParams.get("groupBy") as CostGroupBy | null;
  const groupBy: CostGroupBy = dimensions.some((d) => d.key === requested)
    ? (requested as CostGroupBy)
    : projectScoped
      ? "agent"
      : "project";
  const picked = searchParams.getAll("pick");
  const dimension = dimensions.find((d) => d.key === groupBy) ?? dimensions[0];

  // Display names for what cost data names by handle (UX: the names used elsewhere).
  const providers = useListLLMProviders({ orgName });
  const projects = useListProjects({ orgName }, { limit: 100 });
  const orgAgents = useListOrgAgents({ orgName });
  const names = useMemo<CostNames>(
    () => ({
      llmProvider: Object.fromEntries(
        (providers.data?.providers ?? []).map((p) => [p.id, p.name || p.id]),
      ),
      project: Object.fromEntries(
        (projects.data?.projects ?? []).map((p) => [p.name, p.displayName || p.name]),
      ),
      agent: Object.fromEntries(
        (orgAgents.data?.agents ?? []).map((a) => [
          `${a.projectName}/${a.name}`,
          a.displayName || a.name,
        ]),
      ),
    }),
    [providers.data, projects.data, orgAgents.data],
  );
  const { data, isLoading, error } = useCosts(
    { orgName },
    { groupBy, filter: projectName ? [`project:${projectName}`] : undefined },
  );

  const setFilters = (nextGroupBy: CostGroupBy, nextPicked: string[]) => {
    const params = new URLSearchParams(searchParams);
    params.set("groupBy", nextGroupBy);
    params.delete("pick");
    nextPicked.forEach((p) => params.append("pick", p));
    setSearchParams(params, { replace: true });
  };

  const allGroups = useMemo(() => data?.costByGroup ?? [], [data]);
  const months = useMemo(() => keptMonths(allGroups), [allGroups]);
  const options = useMemo(
    () =>
      allGroups
        .map((g) => groupKey(g, groupBy))
        .sort((a, b) =>
          groupLabel(a, groupBy, projectScoped, names).localeCompare(
            groupLabel(b, groupBy, projectScoped, names),
          ),
        ),
    [allGroups, groupBy, projectScoped, names],
  );
  const groups = useMemo(
    () =>
      picked.length
        ? allGroups.filter((g) => picked.includes(groupKey(g, groupBy)))
        : allGroups,
    [allGroups, picked, groupBy],
  );

  const totals = monthlyTotals(groups, months);
  const current = totals[months.length - 1] ?? 0;
  const last = totals[months.length - 2] ?? 0;
  const beforeLast = totals[months.length - 3] ?? 0;
  const threeMonths = totals.reduce((a, b) => a + b, 0);
  const change = changePercent(beforeLast, last);
  const { missingPrice: missing, usageUnavailable: unavailable } = unpricedTotals(groups);

  // The top groups by total, then everything else as one "Other" series.
  const ranked = [...groups].sort((a, b) => amount(b.cost) - amount(a.cost));
  const top = ranked.slice(0, TOP_GROUPS);
  const rest = ranked.slice(TOP_GROUPS);
  const series: (CostSeries & { costs: number[] })[] = top.map((g, i) => ({
    key: `s${i}`,
    name: groupLabel(groupKey(g, groupBy), groupBy, projectScoped, names),
    costs: entryMonthly(g, months),
  }));
  if (rest.length) {
    series.push({
      key: "other",
      name: `Other (${rest.length})`,
      other: true,
      costs: monthlyTotals(rest, months),
    });
  }
  const chartRows: CostChartRow[] = months.map((m, i) => {
    const row: CostChartRow = {
      month: i === months.length - 1 ? `${monthLabel(m)} (so far)` : monthLabel(m),
    };
    series.forEach((s) => {
      row[s.key] = Math.round(s.costs[i] * 100) / 100;
    });
    return row;
  });

  const thisMonth = months.length ? monthLabel(months[months.length - 1]) : "";
  const lastMonth = months.length > 1 ? monthLabel(months[months.length - 2]) : "";
  const firstMonth = months.length ? monthLabel(months[0]) : "";
  const scope = picked.length
    ? picked.map((p) => groupLabel(p, groupBy, projectScoped, names)).join(", ")
    : groupBy === "organization"
      ? "The organization"
      : `All ${dimension.plural.toLowerCase()}`;

  if (error) {
    return (
      <StateNotice
        compact
        icon={ServerCrash}
        tone="error"
        title="Couldn't load cost"
        description="The cost report failed to load. Reload the page to try again."
      />
    );
  }

  return (
    <Stack spacing={2.5}>
      <Stack direction={{ xs: "column", md: "row" }} spacing={1.5} alignItems={{ md: "center" }}>
        <Box sx={{ minWidth: 220 }}>
          <TextInput
            select
            size="small"
            aria-label="Group by"
            value={groupBy}
            onChange={(e) => setFilters(e.target.value as CostGroupBy, [])}
            slotProps={{
              select: {
                renderValue: () => `Group by: ${dimension.label}`,
              },
            }}
          >
            {dimensions.map((d) => (
              <MenuItem key={d.key} value={d.key}>
                {d.label}
              </MenuItem>
            ))}
          </TextInput>
        </Box>
        {groupBy !== "organization" && (
          <Autocomplete
            multiple
            size="small"
            sx={{ minWidth: 280, flex: 1, maxWidth: 640 }}
            options={options}
            value={picked}
            onChange={(_, value) => setFilters(groupBy, value)}
            getOptionLabel={(o) => groupLabel(o, groupBy, projectScoped, names)}
            renderInput={(params) => (
              <TextField
                {...params}
                placeholder={picked.length ? `Add ${dimension.label.toLowerCase()}` : `All ${dimension.plural.toLowerCase()}`}
                slotProps={{ htmlInput: { ...params.inputProps, "aria-label": `${dimension.plural} to show` } }}
              />
            )}
          />
        )}
      </Stack>

      <Typography variant="body2" color="text.secondary">
        Showing <strong>{scope}</strong>
        {projectName ? ` in ${projectName}` : ""}
      </Typography>

      <Grid container spacing={2}>
        {isLoading ? (
          [0, 1, 2, 3].map((i) => (
            <Grid key={i} size={{ xs: 12, sm: 6, lg: 3 }}>
              <Skeleton variant="rounded" height={104} />
            </Grid>
          ))
        ) : (
          <>
            <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
              <Tile label={`This month (${thisMonth})`} value={formatUsd(current)} caption={`So far in ${thisMonth}`} highlight />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
              <Tile
                label={`Last month (${lastMonth})`}
                value={formatUsd(last)}
                caption={
                  change === undefined
                    ? undefined
                    : `${change >= 0 ? "+" : ""}${change}% vs ${monthLabel(months[months.length - 3] ?? "")}`
                }
              />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
              <Tile
                label={`Last 3 months (${firstMonth}–${thisMonth})`}
                value={formatUsd(threeMonths)}
                caption={`Includes ${thisMonth} so far`}
              />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
              <Tile
                label={`Requests charged $0 (${firstMonth}–${thisMonth})`}
                value={String(missing + unavailable)}
                caption={`${missing} no price · ${unavailable} no usage`}
              />
            </Grid>
          </>
        )}
      </Grid>

      <Card variant="outlined">
        <CardContent>
          <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 1 }}>
            <Typography variant="subtitle1">Monthly cost (USD)</Typography>
            <AnalyticsLink />
          </Stack>
          {isLoading ? (
            <Skeleton variant="rounded" height={396} />
          ) : groups.length === 0 ? (
            <StateNotice
              compact
              icon={ChartColumnStacked}
              title="No AI traffic yet"
              description="Cost appears here once LLM requests go through a gateway with AI cost tracking on."
            />
          ) : (
            <CostChart rows={chartRows} series={series} />
          )}
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>
            UTC calendar months · Kept for 3 months, including the current one
          </Typography>
        </CardContent>
      </Card>
    </Stack>
  );
}
