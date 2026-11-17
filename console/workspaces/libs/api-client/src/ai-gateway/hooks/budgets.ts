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

import { useAuthHooks } from "@agent-management-platform/auth";
import { useQueryClient } from "@tanstack/react-query";
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
  createBudget,
  deleteBudget,
  getCosts,
  getBudget,
  getBudgetUsage,
  listBudgets,
  updateBudget,
} from "../apis/budgets";
import { useApiMutation, useApiQuery } from "../../shared/react-query-notifications";
import { useUIModulePresent } from "../../shared/useUIModulePresent";

export function useListBudgets(params: ListBudgetsPathParams, query?: ListBudgetsQuery) {
  const { getToken } = useAuthHooks();
  const gatewayPresent = useUIModulePresent("ai-gateway");
  return useApiQuery<BudgetListResponse>({
    queryKey: ["budgets", params, query],
    queryFn: () => listBudgets(params, query, getToken),
    enabled: gatewayPresent && !!params.orgName,
  });
}

export function useGetBudget(params: BudgetPathParams) {
  const { getToken } = useAuthHooks();
  const gatewayPresent = useUIModulePresent("ai-gateway");
  return useApiQuery<Budget>({
    queryKey: ["budget", params],
    queryFn: () => getBudget(params, getToken),
    enabled: gatewayPresent && !!params.orgName && !!params.budgetName,
  });
}

export function useBudgetUsage(params: BudgetPathParams, query?: ListQuery) {
  const { getToken } = useAuthHooks();
  const gatewayPresent = useUIModulePresent("ai-gateway");
  return useApiQuery<BudgetUsageListResponse>({
    queryKey: ["budget-usage", params, query],
    queryFn: () => getBudgetUsage(params, query, getToken),
    enabled: gatewayPresent && !!params.orgName && !!params.budgetName,
  });
}

export function useCreateBudget(params: ListBudgetsPathParams) {
  const { getToken } = useAuthHooks();
  const queryClient = useQueryClient();
  return useApiMutation<Budget, unknown, Budget>({
    action: { verb: "create", target: "budget" },
    mutationFn: (body) => createBudget(params, body, getToken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["budgets"] });
    },
  });
}

export function useUpdateBudget(params: BudgetPathParams) {
  const { getToken } = useAuthHooks();
  const queryClient = useQueryClient();
  return useApiMutation<Budget, unknown, Budget>({
    action: { verb: "update", target: "budget" },
    mutationFn: (body) => updateBudget(params, body, getToken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["budgets"] });
      queryClient.invalidateQueries({ queryKey: ["budget", params] });
    },
  });
}

export function useDeleteBudget() {
  const { getToken } = useAuthHooks();
  const queryClient = useQueryClient();
  return useApiMutation<void, unknown, BudgetPathParams>({
    action: { verb: "delete", target: "budget" },
    mutationFn: (params) => deleteBudget(params, getToken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["budgets"] });
    },
  });
}

export function useCosts(params: GetCostsPathParams, query?: GetCostsQuery) {
  const { getToken } = useAuthHooks();
  const gatewayPresent = useUIModulePresent("ai-gateway");
  return useApiQuery<CostReport>({
    queryKey: ["costs", params, query],
    queryFn: () => getCosts(params, query, getToken),
    enabled: gatewayPresent && !!params.orgName,
  });
}
