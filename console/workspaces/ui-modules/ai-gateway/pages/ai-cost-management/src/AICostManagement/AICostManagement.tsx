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

import React from "react";
import { ListingTable } from "@wso2/oxygen-ui";
import {
  generatePath,
  Navigate,
  Route,
  Routes,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { PageLayout } from "@agent-management-platform/views";
import { useEntityNames } from "./useEntityNames";
import { useFeatureManagement } from "@agent-management-platform/feature-management";
import { absoluteRouteMap } from "@agent-management-platform/types";
import { CostOverview } from "./subComponents/CostOverview";
import { BudgetsTab } from "./subComponents/BudgetsTab";
import { BudgetDetail } from "./subComponents/BudgetDetail";
import { BudgetWizard } from "./subComponents/BudgetForm";
import { UnpricedRequests } from "./subComponents/UnpricedRequests";

type View = "overview" | "budgets";

/** The organization's Cost Management, or a project's when the URL has one. */
function useScope() {
  const { orgId, projectId } = useParams<{
    orgId: string;
    projectId?: string;
  }>();
  const route = projectId
    ? absoluteRouteMap.children.org.children.projects.children.aiCostManagement
    : absoluteRouteMap.children.org.children.aiCostManagement;
  const params = { orgId, projectId };
  const budgets = route.children.budgets;
  return {
    orgId,
    projectId,
    overview: generatePath(route.path, params),
    unpriced: generatePath(route.children.unpricedRequests.path, params),
    budgets: generatePath(budgets.path, params),
    budgetCreate: generatePath(budgets.children.create.path, params),
    budgetView: (budgetName: string) =>
      generatePath(budgets.children.view.path, { ...params, budgetName }),
    budgetEdit: (budgetName: string) =>
      generatePath(budgets.children.view.children.edit.path, {
        ...params,
        budgetName,
      }),
  };
}

const TITLES: Record<View, string> = {
  overview: "Dashboard",
  budgets: "Budgets",
};

function describe(
  view: View,
  projectId?: string,
  projectName?: string,
): React.ReactNode {
  const project = projectName ?? projectId;
  if (view === "overview") {
    return projectId ? (
      <span>
        What LLM traffic in project <strong>{project}</strong> cost per month.
      </span>
    ) : (
      "What LLM traffic across the organization cost per month."
    );
  }
  return projectId ? (
    <span>
      Budgets that apply to project <strong>{project}</strong>. Budgets are
      managed by the organization.
    </span>
  ) : (
    "Budgets that cap what LLM traffic may spend, with what they spent this period."
  );
}

function CostManagementPage({ view }: { view: View }) {
  const navigate = useNavigate();
  const { orgId, projectId, budgetView, budgetCreate, unpriced } = useScope();
  const projectName = useEntityNames(orgId ?? "").project?.[projectId ?? ""];
  return (
    <PageLayout
      title={TITLES[view]}
      description={describe(view, projectId, projectName)}
      disableIcon
    >
      <ListingTable.Container>
        {view === "overview" ? (
          <CostOverview
            orgName={orgId}
            projectName={projectId}
            unpricedHref={unpriced}
          />
        ) : (
          <BudgetsTab
            orgName={orgId}
            projectName={projectId}
            onOpen={(name) => navigate(budgetView(name))}
            onCreate={() => navigate(budgetCreate)}
          />
        )}
      </ListingTable.Container>
    </PageLayout>
  );
}

/** A page shown only with its access; otherwise the page the user may open instead. */
function Guard({
  allowed,
  fallback,
  children,
}: {
  allowed: boolean;
  fallback: string;
  children: React.ReactElement;
}) {
  return allowed ? children : <Navigate to={fallback} replace />;
}

function UnpricedRequestsPage() {
  const { orgId, projectId, overview } = useScope();
  const [searchParams] = useSearchParams();
  const query = searchParams.toString();
  return (
    <UnpricedRequests
      orgName={orgId}
      projectName={projectId}
      backHref={query ? `${overview}?${query}` : overview}
    />
  );
}

function BudgetDetailPage() {
  const { budgetName = "" } = useParams<{ budgetName: string }>();
  const { orgId, projectId, budgets, budgetEdit, unpriced } = useScope();
  return (
    <BudgetDetail
      orgName={orgId}
      budgetName={budgetName}
      backHref={budgets}
      editHref={budgetEdit(budgetName)}
      readOnly={!!projectId}
      unpricedHref={unpriced}
    />
  );
}

/** The create wizard, or with `editing` the edit wizard of the budget in the URL. */
function BudgetFormRoute({ editing }: { editing?: boolean }) {
  const navigate = useNavigate();
  const { budgetName } = useParams<{ budgetName: string }>();
  const { orgId, budgets, budgetView } = useScope();
  return (
    <BudgetWizard
      orgName={orgId}
      budgetName={editing ? budgetName : undefined}
      backHref={editing && budgetName ? budgetView(budgetName) : budgets}
      backLabel={editing ? "Back to the budget" : "Back to budgets"}
      onSaved={(name) => navigate(budgetView(name))}
    />
  );
}

/** Cost Management for an organization or one of its projects; the left
 * navigation's Cost Management section opens each view. */
export const AICostManagement: React.FC = () => {
  const { overview, budgets, projectId } = useScope();
  const { hasAccess } = useFeatureManagement();
  const canReadCost = hasAccess("ai_cost_read");
  const canReadBudgets = hasAccess("budget_read");
  // The module opens with either access (plugin.ts); each page checks its own.
  const home = canReadCost ? overview : budgets;
  return (
    <Routes>
      <Route
        index
        element={
          <Guard allowed={canReadCost} fallback={budgets}>
            <CostManagementPage view="overview" />
          </Guard>
        }
      />
      <Route
        path="unpriced-requests"
        element={
          <Guard allowed={canReadCost} fallback={home}>
            <UnpricedRequestsPage />
          </Guard>
        }
      />
      <Route
        path="budgets"
        element={
          <Guard allowed={canReadBudgets} fallback={overview}>
            <CostManagementPage view="budgets" />
          </Guard>
        }
      />
      <Route
        path="budgets/create"
        element={
          <Guard
            allowed={hasAccess("budget_create") && !projectId}
            fallback={budgets}
          >
            <BudgetFormRoute />
          </Guard>
        }
      />
      <Route
        path="budgets/view/:budgetName"
        element={
          <Guard allowed={canReadBudgets} fallback={overview}>
            <BudgetDetailPage />
          </Guard>
        }
      />
      <Route
        path="budgets/view/:budgetName/edit"
        element={<BudgetEditRoute />}
      />
      <Route path="*" element={<Navigate to={home} replace />} />
    </Routes>
  );
};

/** Edit, for users who may update budgets; others go to the budget's page. */
function BudgetEditRoute() {
  const { budgetName = "" } = useParams<{ budgetName: string }>();
  const { budgetView, projectId } = useScope();
  // Budgets are managed at organization level only.
  const allowed =
    useFeatureManagement().hasAccess("budget_update") && !projectId;
  return (
    <Guard allowed={allowed} fallback={budgetView(budgetName)}>
      <BudgetFormRoute editing />
    </Guard>
  );
}

export default AICostManagement;
