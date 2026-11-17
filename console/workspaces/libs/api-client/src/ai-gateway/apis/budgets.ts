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

import type {
  Budget,
  BudgetListResponse,
  BudgetPathParams,
  BudgetUsageListResponse,
  CostReport,
  GetCostsPathParams,
  GetCostsQuery,
  ListBudgetsPathParams,
  ListBudgetsQuery,
  ListQuery,
} from "@agent-management-platform/types";
import {
  encodeRequired,
  httpDELETE,
  httpGET,
  httpPOST,
  httpPUT,
  SERVICE_BASE,
} from "../../shared/utils";

export async function listBudgets(
  params: ListBudgetsPathParams,
  query?: ListBudgetsQuery,
  getToken?: () => Promise<string>,
): Promise<BudgetListResponse> {
  const org = encodeRequired(params.orgName, "orgName");
  const token = getToken ? await getToken() : undefined;
  const searchParams: Record<string, string> = {};
  if (query?.projectId) searchParams.projectId = query.projectId;
  if (query?.filter) searchParams.filter = query.filter;
  if (query?.limit !== undefined) searchParams.limit = String(query.limit);
  if (query?.offset !== undefined) searchParams.offset = String(query.offset);

  const res = await httpGET(`${SERVICE_BASE}/orgs/${org}/budgets`, { searchParams, token });
  if (!res.ok) throw await res.json();
  return res.json();
}

export async function getBudget(
  params: BudgetPathParams,
  getToken?: () => Promise<string>,
): Promise<Budget> {
  const org = encodeRequired(params.orgName, "orgName");
  const name = encodeRequired(params.budgetName, "budgetName");
  const token = getToken ? await getToken() : undefined;
  const res = await httpGET(`${SERVICE_BASE}/orgs/${org}/budgets/${name}`, { token });
  if (!res.ok) throw await res.json();
  return res.json();
}

export async function createBudget(
  params: ListBudgetsPathParams,
  body: Budget,
  getToken?: () => Promise<string>,
): Promise<Budget> {
  const org = encodeRequired(params.orgName, "orgName");
  const token = getToken ? await getToken() : undefined;
  const res = await httpPOST(`${SERVICE_BASE}/orgs/${org}/budgets`, body, { token });
  if (!res.ok) throw await res.json();
  return res.json();
}

export async function updateBudget(
  params: BudgetPathParams,
  body: Budget,
  getToken?: () => Promise<string>,
): Promise<Budget> {
  const org = encodeRequired(params.orgName, "orgName");
  const name = encodeRequired(params.budgetName, "budgetName");
  const token = getToken ? await getToken() : undefined;
  const res = await httpPUT(`${SERVICE_BASE}/orgs/${org}/budgets/${name}`, body, { token });
  if (!res.ok) throw await res.json();
  return res.json();
}

export async function deleteBudget(
  params: BudgetPathParams,
  getToken?: () => Promise<string>,
): Promise<void> {
  const org = encodeRequired(params.orgName, "orgName");
  const name = encodeRequired(params.budgetName, "budgetName");
  const token = getToken ? await getToken() : undefined;
  const res = await httpDELETE(`${SERVICE_BASE}/orgs/${org}/budgets/${name}`, { token });
  if (!res.ok) throw await res.json();
}

/** Each allowance's spend this period, highest first. */
export async function getBudgetUsage(
  params: BudgetPathParams,
  query?: ListQuery,
  getToken?: () => Promise<string>,
): Promise<BudgetUsageListResponse> {
  const org = encodeRequired(params.orgName, "orgName");
  const name = encodeRequired(params.budgetName, "budgetName");
  const token = getToken ? await getToken() : undefined;
  const searchParams: Record<string, string> = {};
  if (query?.limit !== undefined) searchParams.limit = String(query.limit);
  if (query?.offset !== undefined) searchParams.offset = String(query.offset);
  const res = await httpGET(`${SERVICE_BASE}/orgs/${org}/budgets/${name}/budget-usage`, {
    searchParams,
    token,
  });
  if (!res.ok) throw await res.json();
  return res.json();
}

/** Cost per month for the kept months, grouped by one dimension. */
export async function getCosts(
  params: GetCostsPathParams,
  query?: GetCostsQuery,
  getToken?: () => Promise<string>,
): Promise<CostReport> {
  const org = encodeRequired(params.orgName, "orgName");
  const token = getToken ? await getToken() : undefined;
  // filter repeats (AND), so the search params are a list of pairs.
  const searchParams: string[][] = [];
  if (query?.groupBy) searchParams.push(["groupBy", query.groupBy]);
  for (const f of query?.filter ?? []) searchParams.push(["filter", f]);
  const res = await httpGET(`${SERVICE_BASE}/orgs/${org}/costs`, { searchParams, token });
  if (!res.ok) throw await res.json();
  return res.json();
}
