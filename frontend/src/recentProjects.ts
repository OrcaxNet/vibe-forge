export const RECENT_PROJECTS_COLLAPSED_LIMIT = 6;

export function visibleRecentProjects<T>(
  projects: readonly T[],
  expanded: boolean,
): readonly T[] {
  return expanded
    ? projects
    : projects.slice(0, RECENT_PROJECTS_COLLAPSED_LIMIT);
}

export function shouldShowRecentProjectsToggle(
  state: "loading" | "ready" | "error",
  projectCount: number,
): boolean {
  return state === "ready" && projectCount > RECENT_PROJECTS_COLLAPSED_LIMIT;
}
