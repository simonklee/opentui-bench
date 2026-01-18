import { createMemo } from "solid-js";
import type { Accessor } from "solid-js";
import { globalCategory, globalFilter } from "../store";

type BenchmarkLike = {
  name: string;
  category: string;
};

export function useFilteredBenchmarks<T extends BenchmarkLike>(
  data: Accessor<T[] | undefined | null>,
) {
  const categories = createMemo(() => {
    const items = data() ?? [];
    return [...new Set(items.map((item) => item.category))];
  });

  const filteredResults = createMemo(() => {
    let items = data() ?? [];

    const category = globalCategory();
    if (category) {
      items = items.filter((item) => item.category === category);
    }

    const filter = globalFilter();
    if (filter) {
      const term = filter.toLowerCase();
      items = items.filter(
        (item) =>
          item.name.toLowerCase().includes(term) || item.category.toLowerCase().includes(term),
      );
    }

    return items;
  });

  return { filteredResults, categories };
}
