/** Pages of a static export take their object from the query string. */
export const machinePath = (id: string): string => `/machine/?id=${encodeURIComponent(id)}`;
