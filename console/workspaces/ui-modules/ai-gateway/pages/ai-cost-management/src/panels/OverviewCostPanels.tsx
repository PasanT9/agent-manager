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

// Cost sections contributed to other pages' overviews (see plugin.ts), and the
// one the LLM provider page shows. Each opens the Dashboard picked to its entity.
import React, { useState, type ReactNode } from "react";
import {
  Box,
  Button,
  Card,
  CardContent,
  Divider,
  Skeleton,
  Typography,
} from "@wso2/oxygen-ui";
import { ChevronRight, Plus } from "@wso2/oxygen-ui-icons-react";
import { useFeatureManagement } from "@agent-management-platform/feature-management";
import { generatePath, Link } from "react-router-dom";
import { absoluteRouteMap } from "@agent-management-platform/types";
import {
  type AgentOverviewPanelProps,
  type ProjectOverviewPanelProps,
} from "@agent-management-platform/shared-component";
import { formatUsd } from "../AICostManagement/costFormat";
import { useEntityNames } from "../AICostManagement/useEntityNames";
import { EntityCostTiles, useEntityCost } from "./EntityCostPanel";
import {
  QuickBudgetDialog,
  type QuickBudgetDialogProps,
} from "./QuickBudgetDialog";

const orgNav = absoluteRouteMap.children.org.children;

type AddBudgetProps = Omit<QuickBudgetDialogProps, "open" | "onClose">;

/** "Add budget" and its quick form, for users who may create budgets. */
function AddBudget(props: AddBudgetProps): ReactNode {
  const canWrite = useFeatureManagement().hasAccess("budget_create");
  const [open, setOpen] = useState(false);
  if (!canWrite) return null;
  return (
    <>
      <Button
        size="small"
        variant="outlined"
        startIcon={<Plus size={14} />}
        onClick={() => setOpen(true)}
        sx={{ borderRadius: 999, textTransform: "none", whiteSpace: "nowrap" }}
      >
        Add budget
      </Button>
      {open && (
        <QuickBudgetDialog {...props} open onClose={() => setOpen(false)} />
      )}
    </>
  );
}

/** Shared section header for the agent and LLM provider cost summaries. */
function CostSectionHeader({ add }: { add: ReactNode }) {
  return (
    <Box
      display="flex"
      justifyContent="space-between"
      alignItems="center"
      mb={1.5}
    >
      <Typography
        variant="subtitle2"
        color="text.secondary"
        sx={{ fontWeight: 600 }}
      >
        Cost
      </Typography>
      {add}
    </Box>
  );
}

/** "View cost details", below a Cost section's figures. */
function ViewCostDetails({ href }: { href: string }) {
  return (
    <Box sx={{ mt: 1 }}>
      <Button
        component={Link}
        to={href}
        size="small"
        variant="text"
        endIcon={<ChevronRight size={14} />}
        sx={{ minWidth: 0, px: 0, fontSize: "0.75rem" }}
      >
        View cost details
      </Button>
    </Box>
  );
}

/** Budget pages at org level, or under a project. */
function budgetRoutes(orgId: string, projectId?: string) {
  const page = projectId
    ? orgNav.projects.children.aiCostManagement
    : orgNav.aiCostManagement;
  const params = { orgId, projectId };
  return {
    createHref: generatePath(
      page.children.budgets.children.create.path,
      params,
    ),
    viewHref: (budgetName: string) =>
      generatePath(page.children.budgets.children.view.path, {
        ...params,
        budgetName,
      }),
  };
}

/** The Dashboard, grouped by `groupBy` and picked to `pick`. */
function dashboardHref(base: string, groupBy?: string, pick?: string): string {
  if (!groupBy || !pick) return base;
  const q = new URLSearchParams({ groupBy, pick });
  return `${base}?${q.toString()}`;
}

function CostLine({
  label,
  value,
  caption,
  highlight,
  loading,
}: {
  label: string;
  value: string;
  caption?: string;
  highlight?: boolean;
  loading?: boolean;
}) {
  return (
    <Box
      display="flex"
      justifyContent="space-between"
      alignItems="flex-start"
      gap={2}
    >
      <Box>
        <Typography variant="body2">{label}</Typography>
        {caption && (
          <Typography variant="caption" color="text.secondary">
            {caption}
          </Typography>
        )}
      </Box>
      {loading ? (
        <Skeleton variant="text" width={64} />
      ) : (
        <Typography
          variant="body2"
          fontWeight={highlight ? 600 : 400}
          sx={{
            color: highlight ? "primary.main" : "text.primary",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {value}
        </Typography>
      )}
    </Box>
  );
}

/** A project's cost, in its overview's side column. */
export const ProjectCostCard: React.FC<ProjectOverviewPanelProps> = ({
  orgId,
  projectId,
}) => {
  const c = useEntityCost(orgId, `project:${projectId}`);
  const projectName = useEntityNames(orgId).project?.[projectId] ?? projectId;
  const href = generatePath(orgNav.projects.children.aiCostManagement.path, {
    orgId,
    projectId,
  });
  return (
    <Card variant="outlined" sx={{ minWidth: 300, flexGrow: 1 }}>
      <CardContent>
        <Box display="flex" flexDirection="column" gap={1.5}>
          <Box
            display="flex"
            justifyContent="space-between"
            alignItems="center"
          >
            <Typography variant="h6">Cost</Typography>
            <AddBudget
              orgName={orgId}
              start={{ project: projectId }}
              kind="Project"
              label={projectName}
              projectOwner={projectId}
              {...budgetRoutes(orgId, projectId)}
            />
          </Box>
          {c.error ? (
            <Typography variant="body2" color="text.secondary">
              Couldn&apos;t load cost. Reload the page to try again.
            </Typography>
          ) : (
            <>
              <CostLine
                label={`This month (${c.thisMonth})`}
                caption="So far"
                value={formatUsd(c.current)}
                highlight
                loading={c.isLoading}
              />
              <Divider />
              <CostLine
                label={`Last month (${c.lastMonth})`}
                caption={c.changeText}
                value={formatUsd(c.last)}
                loading={c.isLoading}
              />
              <Divider />
              <CostLine
                label={`Last 3 months (${c.firstMonth}–${c.thisMonth})`}
                caption={`Includes ${c.thisMonth} so far`}
                value={formatUsd(c.window)}
                loading={c.isLoading}
              />
            </>
          )}
          <ViewCostDetails href={href} />
        </Box>
      </CardContent>
    </Card>
  );
};

/** An agent's cost, as a section of its overview. */
export const AgentCostSection: React.FC<AgentOverviewPanelProps> = ({
  orgId,
  projectId,
  agentId,
}) => {
  const base = generatePath(orgNav.projects.children.aiCostManagement.path, {
    orgId,
    projectId,
  });
  const agentName =
    useEntityNames(orgId).agent?.[`${projectId}/${agentId}`] ?? agentId;
  return (
    <Box sx={{ width: "100%" }}>
      <CostSectionHeader
        add={
          <AddBudget
            orgName={orgId}
            start={{ agent: `${projectId}/${agentId}` }}
            kind="Agent"
            label={agentName}
            projectOwner={projectId}
            {...budgetRoutes(orgId, projectId)}
          />
        }
      />
      <EntityCostTiles
        orgName={orgId}
        filter={`agent:${projectId}/${agentId}`}
        fill
      />
      <ViewCostDetails
        href={dashboardHref(base, "agent", `${projectId}/${agentId}`)}
      />
    </Box>
  );
};

/**
 * An LLM provider's cost, a section of its Overview tab, headed as the tab's other
 * sections (Invoke URL & API Key).
 */
export const LLMProviderCostSection: React.FC<{
  orgId: string;
  /** The provider's handle, as budgets and cost name it. */
  providerId: string;
  /** Shown to the user. */
  providerName?: string;
}> = ({ orgId, providerId, providerName }) => {
  const base = generatePath(orgNav.aiCostManagement.path, { orgId });
  return (
    <Box sx={{ width: "100%" }}>
      <CostSectionHeader
        add={
          <AddBudget
            orgName={orgId}
            start={{ provider: providerId }}
            kind="LLM provider"
            label={providerName ?? providerId}
            {...budgetRoutes(orgId)}
          />
        }
      />
      <EntityCostTiles
        orgName={orgId}
        filter={`llmProvider:${providerId}`}
        fill
      />
      <ViewCostDetails href={dashboardHref(base, "llmProvider", providerId)} />
    </Box>
  );
};
