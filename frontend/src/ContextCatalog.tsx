import { useMemo, useState } from "react";
import {
  CONTEXT_COMPONENTS,
  filterContextComponents,
  scanInjection,
  type ContextComponent,
  type ContextComponentType,
} from "./contextCatalogData";

type ContextCatalogProps = {
  componentId?: string;
  onHome: () => void;
  onSelect: (componentId: string) => void;
};

function CatalogIcon({
  name,
  className = "h-4 w-4",
}: {
  name: "arrow" | "braces" | "file" | "search" | "shield" | "tool";
  className?: string;
}) {
  const paths = {
    arrow: <path d="M19 12H5m6 6-6-6 6-6" />,
    braces: <path d="M8 3H6a2 2 0 0 0-2 2v5l-2 2 2 2v5a2 2 0 0 0 2 2h2m8-18h2a2 2 0 0 1 2 2v5l2 2-2 2v5a2 2 0 0 1-2 2h-2" />,
    file: <path d="M6 2h8l4 4v16H6V2Zm8 0v5h5M9 13h6m-6 4h6" />,
    search: <path d="m21 21-4.35-4.35m2.35-5.15A7.5 7.5 0 1 1 4 11.5a7.5 7.5 0 0 1 15 0Z" />,
    shield: <path d="M12 22s8-3.8 8-10V5l-8-3-8 3v7c0 6.2 8 10 8 10Zm-4-10 2.5 2.5L16 9" />,
    tool: <path d="M14.7 6.3a4 4 0 0 0-5-5L12 3.6 9.6 6 7.3 3.7a4 4 0 0 0 5 5L5 16l3 3 7.3-7.3a4 4 0 0 0 5-5L18 9l-2.4-2.4 2.3-2.3a4 4 0 0 0-3.2 2Z" />,
  };
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {paths[name]}
    </svg>
  );
}

function TypeIcon({ type }: { type: ContextComponentType }) {
  return type === "tool-description" ? (
    <CatalogIcon name="tool" />
  ) : (
    <CatalogIcon name="file" />
  );
}

function StatusDot({ status }: { status: ContextComponent["status"] }) {
  const color = {
    published: "bg-[#23886f]",
    reviewed: "bg-[#be7a16]",
    draft: "bg-[#9aa3ad]",
  }[status];
  return (
    <span className="inline-flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-[0.12em] text-[#6d756f]">
      <span className={`h-1.5 w-1.5 rounded-full ${color}`} />
      {status}
    </span>
  );
}

export default function ContextCatalog({
  componentId,
  onHome,
  onSelect,
}: ContextCatalogProps) {
  const [query, setQuery] = useState("");
  const [type, setType] = useState<"all" | ContextComponentType>("all");
  const selected =
    CONTEXT_COMPONENTS.find((component) => component.id === componentId) ??
    CONTEXT_COMPONENTS[0];
  const [storyId, setStoryId] = useState(selected.stories[0]?.id ?? "");

  const result = useMemo(
    () => filterContextComponents(CONTEXT_COMPONENTS, query, type),
    [query, type],
  );
  const selectedStory =
    selected.stories.find((story) => story.id === storyId) ??
    selected.stories[0];
  const injectionHits = selectedStory
    ? scanInjection(selectedStory.renderedContent)
    : [];

  const selectComponent = (next: ContextComponent) => {
    setStoryId(next.stories[0]?.id ?? "");
    onSelect(next.id);
  };

  const groups = (["tool-description", "prompt"] as const)
    .map((groupType) => ({
      type: groupType,
      items: result.items.filter((component) => component.type === groupType),
    }))
    .filter((group) => group.items.length > 0);

  return (
    <div className="min-h-screen bg-[#eef0eb] font-sans text-[#17201c]">
      <header className="flex min-h-16 flex-wrap items-center gap-4 border-b border-[#cfd5ce] bg-[#f8f8f4] px-4 py-3 sm:px-6">
        <button
          type="button"
          onClick={onHome}
          className="inline-flex min-h-10 items-center gap-2 rounded-lg px-2 text-sm font-bold text-[#47524c] transition hover:bg-[#e8ebe5] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#196b59]"
        >
          <CatalogIcon name="arrow" />
          Vibe Forge
        </button>
        <div className="h-7 w-px bg-[#d6dad4]" />
        <div className="flex items-center gap-3">
          <span className="grid h-9 w-9 place-items-center rounded-lg bg-[#164f43] text-[#f4fbf8] shadow-[inset_0_0_0_1px_rgba(255,255,255,.15)]">
            <CatalogIcon name="braces" />
          </span>
          <div>
            <p className="text-sm font-black tracking-[-0.02em]">CONTEXT CATALOG</p>
            <p className="font-mono text-[9px] font-bold uppercase tracking-[0.18em] text-[#707a74]">
              Component-driven context
            </p>
          </div>
        </div>
        <div className="ml-auto flex items-center gap-3">
          <span className="hidden rounded-full border border-[#bdd2ca] bg-[#e7f2ed] px-3 py-1 font-mono text-[10px] font-bold text-[#196b59] sm:inline-flex">
            MILESTONE 01 · LOCAL DATA
          </span>
          <span className="font-mono text-[10px] text-[#6f7973]">
            {CONTEXT_COMPONENTS.length} components
          </span>
        </div>
      </header>

      <main className="grid min-h-[calc(100vh-4rem)] grid-cols-1 lg:grid-cols-[300px_minmax(0,1fr)_300px]">
        <aside className="border-b border-[#cfd5ce] bg-[#f6f7f2] lg:border-b-0 lg:border-r">
          <div className="border-b border-[#d9ddd7] p-4">
            <label className="relative block">
              <span className="sr-only">Search context components</span>
              <CatalogIcon
                name="search"
                className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[#748078]"
              />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search name, owner, tag…"
                className="h-10 w-full rounded-lg border border-[#cbd1ca] bg-white pl-9 pr-3 text-sm outline-none transition placeholder:text-[#929b95] focus:border-[#2b7867] focus:ring-2 focus:ring-[#2b7867]/15"
              />
            </label>
            <div className="mt-3 grid grid-cols-3 gap-1 rounded-lg bg-[#e7eae5] p-1">
              {(["all", "tool-description", "prompt"] as const).map(
                (filterType) => (
                  <button
                    key={filterType}
                    type="button"
                    onClick={() => setType(filterType)}
                    className={`min-h-8 rounded-md px-1 text-[10px] font-black transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-[#196b59] ${
                      type === filterType
                        ? "bg-white text-[#165a4b] shadow-sm"
                        : "text-[#69736d] hover:text-[#26332d]"
                    }`}
                  >
                    {filterType === "all"
                      ? "ALL"
                      : filterType === "prompt"
                        ? "PROMPT"
                        : "TOOL"}
                  </button>
                ),
              )}
            </div>
            <div
              className="mt-3 flex items-center justify-between font-mono text-[10px] text-[#747e78]"
              aria-live="polite"
            >
              <span>{result.items.length} matches</span>
              <span data-testid="search-timing">
                search {result.elapsedMs.toFixed(2)} ms
              </span>
            </div>
          </div>

          <div className="max-h-[430px] overflow-y-auto p-2 lg:max-h-[calc(100vh-13.25rem)]">
            {groups.length === 0 ? (
              <div className="m-2 rounded-lg border border-dashed border-[#c8cec7] px-4 py-8 text-center text-sm text-[#6f7973]">
                No component matches this filter.
              </div>
            ) : (
              groups.map((group) => (
                <section key={group.type} className="mb-4">
                  <div className="flex items-center gap-2 px-2 py-1.5 text-[10px] font-black uppercase tracking-[0.14em] text-[#68736c]">
                    <TypeIcon type={group.type} />
                    <span>{group.type}</span>
                    <span className="ml-auto font-mono">{group.items.length}</span>
                  </div>
                  <ul className="space-y-1">
                    {group.items.map((component) => (
                      <li key={component.id}>
                        <button
                          type="button"
                          aria-current={
                            component.id === selected.id ? "page" : undefined
                          }
                          onClick={() => selectComponent(component)}
                          className={`w-full rounded-lg border px-3 py-2.5 text-left transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[#196b59] ${
                            component.id === selected.id
                              ? "border-[#97b9ac] bg-[#deebe5] shadow-[inset_3px_0_0_#196b59]"
                              : "border-transparent hover:border-[#d6dad4] hover:bg-white"
                          }`}
                        >
                          <span className="block truncate text-sm font-extrabold">
                            {component.name}
                          </span>
                          <span className="mt-1 flex items-center justify-between gap-2">
                            <span className="text-[10px] text-[#727c76]">
                              {component.stories.length} stories
                            </span>
                            <StatusDot status={component.status} />
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              ))
            )}
          </div>
        </aside>

        <section className="min-w-0 bg-[#f0f2ed] p-3 sm:p-5">
          <div className="mx-auto flex h-full min-h-[700px] max-w-5xl flex-col overflow-hidden rounded-xl border border-[#c6ccc5] bg-[#fcfcf8] shadow-[0_18px_50px_rgba(39,51,44,.09)]">
            <div className="border-b border-[#d8dcd6] px-4 py-4 sm:px-6">
              <div className="flex flex-wrap items-start gap-4">
                <div className="min-w-0 flex-1">
                  <p className="font-mono text-[10px] font-bold uppercase tracking-[0.16em] text-[#758078]">
                    {selected.type} / v{selected.version}
                  </p>
                  <h1 className="mt-1 truncate text-2xl font-black tracking-[-0.035em] text-[#14211b]">
                    {selected.name}
                  </h1>
                  <p className="mt-2 max-w-2xl text-sm leading-6 text-[#626e67]">
                    {selected.description}
                  </p>
                </div>
                <StatusDot status={selected.status} />
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2 border-b border-[#d8dcd6] bg-[#f5f6f1] px-4 py-2.5 sm:px-6">
              <span className="mr-1 font-mono text-[9px] font-black uppercase tracking-[0.15em] text-[#727d75]">
                Isolated stories
              </span>
              {selected.stories.map((story) => (
                <button
                  key={story.id}
                  type="button"
                  aria-pressed={story.id === selectedStory?.id}
                  onClick={() => setStoryId(story.id)}
                  style={
                    story.id === selectedStory?.id
                      ? {
                          backgroundColor: "#1d6a59",
                          borderColor: "#1d6a59",
                          color: "#ffffff",
                        }
                      : undefined
                  }
                  className={`min-h-8 appearance-none rounded-md border px-2.5 font-mono text-[10px] font-bold transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[#196b59] ${
                    story.id === selectedStory?.id
                      ? ""
                      : "border-[#ccd2cb] bg-white text-[#536159] hover:border-[#8fa99e]"
                  }`}
                >
                  {story.name}
                </button>
              ))}
            </div>

            {selectedStory && (
              <div className="flex min-h-0 flex-1 flex-col p-3 sm:p-5">
                <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border-2 border-[#4f665c] bg-[#14221c]">
                  <div className="flex flex-wrap items-center gap-2 border-b border-[#405249] bg-[#1b2b24] px-4 py-2.5 text-[#dbe8e1]">
                    <span className="inline-flex items-center gap-2 font-mono text-[10px] font-black uppercase tracking-[0.15em]">
                      <CatalogIcon name="shield" className="h-4 w-4 text-[#7ad0b3]" />
                      Containment boundary
                    </span>
                    <span className="ml-auto rounded-full border border-[#456358] px-2 py-1 font-mono text-[9px] text-[#a9c0b5]">
                      TEXT ONLY · NO HTML · NO EVAL
                    </span>
                  </div>
                  <div
                    className={`border-b px-4 py-2.5 text-xs ${
                      injectionHits.length
                        ? "border-[#725b34] bg-[#392f1f] text-[#f2c778]"
                        : "border-[#365348] bg-[#20382e] text-[#9bd7c1]"
                    }`}
                    role="status"
                  >
                    {injectionHits.length > 0
                      ? `${injectionHits.length} injection marker${injectionHits.length === 1 ? "" : "s"} detected. Payload is rendered as inert text.`
                      : "Isolation checks passed. Story content cannot execute in this preview."}
                  </div>
                  <pre
                    data-testid="rendered-content"
                    className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-4 font-mono text-[12px] leading-6 text-[#e4ece7] sm:p-5 sm:text-[13px]"
                  >
                    {selectedStory.renderedContent}
                  </pre>
                  <div className="flex flex-wrap items-center gap-2 border-t border-[#405249] bg-[#1b2b24] px-4 py-2.5 font-mono text-[10px] text-[#a9bcb2]">
                    <span className="rounded border border-[#496158] px-2 py-1">
                      renderedContent
                    </span>
                    <span className="rounded border border-[#496158] px-2 py-1">
                      {selectedStory.tokenCount} tokens
                    </span>
                    <span className="ml-auto text-[#7e9389]">
                      React text escaping
                    </span>
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap items-start gap-3 rounded-lg border border-[#d4d9d3] bg-[#f7f8f4] p-3">
                  <div className="min-w-0 flex-1">
                    <p className="text-xs font-bold text-[#303d36]">
                      {selectedStory.note}
                    </p>
                    <p className="mt-1 font-mono text-[10px] text-[#78827c]">
                      Story args are read-only in this milestone.
                    </p>
                  </div>
                  <code className="max-w-full overflow-auto rounded-md bg-[#e8ebe6] px-2 py-1.5 font-mono text-[10px] text-[#3e4b44]">
                    {JSON.stringify(selectedStory.args)}
                  </code>
                </div>
              </div>
            )}
          </div>
        </section>

        <aside className="border-t border-[#cfd5ce] bg-[#f8f8f4] p-5 lg:border-l lg:border-t-0">
          <p className="font-mono text-[10px] font-black uppercase tracking-[0.16em] text-[#778079]">
            Review notes
          </p>
          <h2 className="mt-2 text-lg font-black tracking-[-0.025em]">
            What this story proves
          </h2>
          <ol className="mt-5 space-y-5">
            {[
              [
                "Isolated variant",
                "Switching a story replaces only renderedContent and its args.",
              ],
              [
                "Visible token budget",
                "Every story exposes the same token-count field used by composition.",
              ],
              [
                "Inert adversarial text",
                "The preview uses React text nodes; tags and instructions never execute.",
              ],
            ].map(([title, description], index) => (
              <li key={title} className="grid grid-cols-[24px_1fr] gap-3">
                <span className="grid h-6 w-6 place-items-center rounded-full border border-[#9bb4aa] font-mono text-[10px] font-bold text-[#17614f]">
                  {index + 1}
                </span>
                <div>
                  <p className="text-sm font-extrabold">{title}</p>
                  <p className="mt-1 text-xs leading-5 text-[#68736c]">
                    {description}
                  </p>
                </div>
              </li>
            ))}
          </ol>

          <div className="mt-7 rounded-lg border border-[#cbd2cb] bg-white p-4">
            <p className="font-mono text-[9px] font-black uppercase tracking-[0.14em] text-[#747e78]">
              Backend boundary
            </p>
            <code className="mt-2 block break-all font-mono text-[11px] font-bold text-[#155d4d]">
              POST /api/v1/components/:id/stories/:sid/render
            </code>
            <p className="mt-2 text-xs leading-5 text-[#69746d]">
              This milestone uses deterministic local fixtures. The UI shape is
              ready to consume FLO-18&apos;s rendered_content and token_count
              response without coupling to Vibe Forge&apos;s current API.
            </p>
          </div>

          <div className="mt-4 flex flex-wrap gap-2">
            {selected.tags.map((tag) => (
              <span
                key={tag}
                className="rounded-full border border-[#cbd2cb] px-2.5 py-1 font-mono text-[9px] font-bold text-[#637068]"
              >
                #{tag}
              </span>
            ))}
          </div>
        </aside>
      </main>
    </div>
  );
}
