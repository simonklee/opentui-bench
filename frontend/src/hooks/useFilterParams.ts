import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { globalCategory, globalFilter, setGlobalCategory, setGlobalFilter } from "../store";

type SearchParams = Record<string, string | string[] | undefined>;

type SetSearchParams = (
  params: Record<string, string | number | boolean | null | undefined>,
  options?: { replace?: boolean },
) => void;

export function useFilterParams(searchParams: SearchParams, setSearchParams: SetSearchParams) {
  const normalizeParam = (value: string | string[] | undefined) => {
    if (Array.isArray(value)) {
      return value[0] ?? "";
    }
    return typeof value === "string" ? value : "";
  };

  const [didInit, setDidInit] = createSignal(false);
  const [didSync, setDidSync] = createSignal(false);

  createEffect(() => {
    const filterParam = searchParams.filter;
    const categoryParam = searchParams.category;

    const normalizedFilter = normalizeParam(filterParam);
    const normalizedCategory = normalizeParam(categoryParam);

    if (typeof filterParam === "string" || Array.isArray(filterParam)) {
      setGlobalFilter(normalizedFilter);
    } else if (didInit()) {
      setGlobalFilter("");
    }

    if (typeof categoryParam === "string" || Array.isArray(categoryParam)) {
      setGlobalCategory(normalizedCategory);
    } else if (didInit()) {
      setGlobalCategory("");
    }

    if (!didInit()) {
      setDidInit(true);
    }
  });

  createEffect(() => {
    const filter = globalFilter();
    const category = globalCategory();

    const timeoutId = window.setTimeout(() => {
      const currentParams = untrack(() => ({ ...searchParams }));
      const currentFilter = normalizeParam(currentParams.filter);
      const currentCategory = normalizeParam(currentParams.category);

      const nextFilter = filter || "";
      const nextCategory = category || "";

      if (currentFilter === nextFilter && currentCategory === nextCategory) {
        if (!didSync()) {
          setDidSync(true);
        }
        return;
      }

      const nextParams: Record<string, string | number | boolean | null | undefined> = {};

      for (const [key, value] of Object.entries(currentParams)) {
        if (Array.isArray(value)) {
          nextParams[key] = value[0];
        } else {
          nextParams[key] = value;
        }
      }

      nextParams.filter = filter ? filter : null;
      nextParams.category = category ? category : null;

      setSearchParams(nextParams, { replace: !didSync() });

      if (!didSync()) {
        setDidSync(true);
      }
    }, 200);

    onCleanup(() => window.clearTimeout(timeoutId));
  });
}
