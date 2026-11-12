import { get } from "@/lib/api/http";
import type { Catalog } from "@/lib/api/types";

export const loadCatalog = (): Promise<Catalog> => get<Catalog>("/api/catalog");
