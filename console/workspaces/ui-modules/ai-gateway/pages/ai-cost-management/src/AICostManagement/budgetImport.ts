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
  BudgetImportRowError,
  BudgetImportRowResult,
  BudgetImportSummary,
} from "@agent-management-platform/types";

/** The largest CSV file the import takes (the API's limit). */
export const IMPORT_MAX_BYTES = 1024 * 1024;

/** Every column the import reads, in order, with two example rows to edit. */
export const IMPORT_TEMPLATE = [
  "name,projectId,displayName,description,amount,period,action,blockAt,llmProvider,llmProviderAllocation,project,projectAllocation,agent,agentAllocation,apiKey,apiKeyAllocation,attributes,exemptions,thresholds,channels",
  "org-monthly-ceiling,,Organization monthly ceiling,All LLM spend,5000.00,month,block,95,,,,,,,,,,,80;100,finance-webhook",
  "api-platform-each-agent,api-platform,Each API Platform agent,,50.00,week,block,,openai,,api-platform,,,separate,,,,,100,finance-webhook",
  "",
].join("\n");

/**
 * A checked row's validation status. Nothing is created while validating, so a valid row
 * reads "Ready to create", never "Created".
 */
export function rowStatus(r: BudgetImportRowResult): {
  valid: boolean;
  label: string;
} {
  if (r.errors?.length) return { valid: false, label: "Invalid" };
  switch (r.action) {
    case "update":
      return { valid: true, label: "Ready to update" };
    case "skip":
      return { valid: true, label: "Will be skipped" };
    default:
      return { valid: true, label: "Ready to create" };
  }
}

/**
 * A row error as the dialog shows it: the CSV column it is about (to find it in the file)
 * and the server's message, except where it names API parameters the dialog offers as a
 * choice.
 */
export function rowError(e: BudgetImportRowError): {
  column?: string;
  message: string;
} {
  if (e.code === "BUDGET_ALREADY_EXISTS") {
    return {
      column: e.column,
      message:
        "A budget with this name already exists. Set Existing budget handling to skip or update it.",
    };
  }
  return { column: e.column, message: e.message };
}

/**
 * The compact result above the rows. Imports are atomic: with any invalid row nothing is
 * imported.
 */
export function validationResult(
  summary: BudgetImportSummary,
  rows: number,
): { severity: "error" | "info" | "success"; title: string; text: string } {
  if (summary.error > 0) {
    return {
      severity: "error",
      title: "Validation failed.",
      text: `${summary.error} of ${rows} ${rows === 1 ? "row" : "rows"} ${summary.error === 1 ? "contains" : "contain"} errors. No budgets have been imported.`,
    };
  }
  if (summary.created + summary.updated === 0) {
    return {
      severity: "info",
      title: "Nothing to import.",
      text: "Every budget in the file already exists and will be skipped.",
    };
  }
  return {
    severity: "success",
    title: "Validation passed.",
    text: `${rows} of ${rows} ${rows === 1 ? "row is" : "rows are"} valid and ready to import.`,
  };
}

/** What the server said about a file it refused as a whole (INVALID_CSV, too large, ...). */
export function importErrorMessage(error: unknown): string {
  const e = error as {
    message?: unknown;
    code?: unknown;
    status?: unknown;
  } | null;
  if (e?.status === 413) return "The file must be at most 1 MB.";
  if (
    typeof e?.message === "string" &&
    e.message &&
    !/^HTTP error/.test(e.message)
  ) {
    return e.message;
  }
  return "Couldn't check the file. Try again.";
}
